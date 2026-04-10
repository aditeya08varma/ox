#!/usr/sbin/dtrace -s
/*
 * trace-auth-json.d
 *
 * Catch the culprit that keeps clobbering ~/.sageox/auth.json (or any file
 * whose path contains "auth.json"). For every suspect syscall we print:
 *   - wall time
 *   - pid / ppid / uid / execname
 *   - the full command line (argv)
 *   - the path / fd / flags / length involved
 *   - a userland stack trace at the point of the write
 *
 * Covers every way a file can get overwritten or destroyed:
 *
 *   direct writers    : open/openat -> write/pwrite/writev/pwritev/ftruncate
 *   atomic writers    : rename / renameat / renameatx_np landing on target
 *   deleters          : unlink / unlinkat
 *   path truncaters   : truncate
 *
 * Works for ox itself (which uses temp+rename via fileutil.AtomicWriteJSON)
 * AND for any stray script, test, or editor that wipes the file directly.
 *
 * ---------------------------------------------------------------------------
 * USAGE (macOS)
 *
 *   sudo dtrace -s scripts/trace-auth-json.d
 *   sudo dtrace -s scripts/trace-auth-json.d -c './ox-tmp login'
 *   sudo dtrace -s scripts/trace-auth-json.d -p <pid>
 *
 * Narrow the match to one absolute path (recommended while repro'ing):
 *
 *   sudo dtrace -s scripts/trace-auth-json.d \
 *       -D TARGET='"/Users/you/.sageox/auth.json"'
 *
 * Tee to a file while you reproduce the bug:
 *
 *   sudo dtrace -s scripts/trace-auth-json.d 2>&1 | tee /tmp/auth-trace.log
 *
 * ---------------------------------------------------------------------------
 * macOS GOTCHAS
 *
 * 1. System Integrity Protection restricts dtrace. For probes against
 *    arbitrary processes and full ustack() symbolication you usually need:
 *        csrutil enable --without dtrace
 *    (run from Recovery). Without that, you'll still see pids/execnames
 *    for your own binaries, but Apple-signed processes may be invisible
 *    and some frames will be raw addresses.
 *
 * 2. Go binaries call into libSystem for file syscalls on darwin, so these
 *    syscall::* probes DO fire for ox. ustack() frames often look like
 *    "ox`runtime.write" / "ox`syscall.write" / "ox`os.(*File).Write" —
 *    that's enough to identify the caller.
 *
 * 3. Stack symbols for stripped Go binaries are imperfect. If a frame shows
 *    as a raw address, cross-reference with:
 *        addr2line -e ./ox-tmp -f 0xADDRESS
 *    or just rebuild with `go build -gcflags=all=-N -l` for unoptimized
 *    frames before repro'ing.
 *
 * 4. The default dtrace strsize is 256 bytes. We bump to 1024 so long paths
 *    aren't truncated. If you still see "..." in paths, raise strsize.
 *
 * 5. This script uses thread-local state keyed by pid+fd. If you repro for
 *    a long time and see "dynamic variable drops", raise dynvarsize below.
 * ---------------------------------------------------------------------------
 */

#pragma D option quiet
#pragma D option switchrate=10hz
#pragma D option bufsize=16m
#pragma D option dynvarsize=32m
#pragma D option strsize=1024
#pragma D option stackframes=64

/*
 * Substring to match against every path we see. Override on the command
 * line with `-D TARGET='"/abs/path/auth.json"'` for a tighter filter.
 */
#ifndef TARGET
#define TARGET "auth.json"
#endif

dtrace:::BEGIN
{
	printf("trace-auth-json: watching for writes to files matching \"%s\"\n", TARGET);
	printf("press Ctrl-C to stop.\n\n");
}

/* =========================================================================
 * open / openat : stash pathname on entry, remember fd on successful return
 * ========================================================================= */

syscall::open:entry,
syscall::open_nocancel:entry
/arg0 != 0/
{
	self->o_path  = copyinstr(arg0);
	self->o_flags = arg1;
}

syscall::openat:entry,
syscall::openat_nocancel:entry
/arg1 != 0/
{
	self->o_path  = copyinstr(arg1);
	self->o_flags = arg2;
}

syscall::open:return,
syscall::open_nocancel:return,
syscall::openat:return,
syscall::openat_nocancel:return
/self->o_path != NULL && strstr(self->o_path, TARGET) != NULL && (int)arg0 >= 0/
{
	printf("==== OPEN   %Y  pid=%d ppid=%d uid=%d  %s  fd=%d flags=0x%x ====\n",
	    walltimestamp, pid, ppid, uid, execname, (int)arg0, self->o_flags);
	printf("  path : %s\n", self->o_path);
	printf("  argv : %S\n", curpsinfo->pr_psargs);
	ustack();
	printf("\n");

	/* Remember this (pid,fd) so subsequent writes on it get traced. */
	tracked_fd[pid, (int)arg0] = 1;
}

/* Always clear the entry-side scratch so we don't leak dyn vars. */
syscall::open:return,
syscall::open_nocancel:return,
syscall::openat:return,
syscall::openat_nocancel:return
{
	self->o_path  = 0;
	self->o_flags = 0;
}

/* =========================================================================
 * write family on a tracked fd : the actual "overwrite" moment
 * ========================================================================= */

syscall::write:entry,
syscall::write_nocancel:entry,
syscall::pwrite:entry,
syscall::pwrite_nocancel:entry,
syscall::writev:entry,
syscall::writev_nocancel:entry,
syscall::pwritev:entry
/tracked_fd[pid, (int)arg0] != 0/
{
	printf("==== WRITE  %Y  pid=%d  %s  fd=%d len=%d ====\n",
	    walltimestamp, pid, execname, (int)arg0, (int)arg2);
	printf("  argv : %S\n", curpsinfo->pr_psargs);
	ustack();
	printf("\n");
}

syscall::ftruncate:entry
/tracked_fd[pid, (int)arg0] != 0/
{
	printf("==== FTRUNC %Y  pid=%d  %s  fd=%d len=%d ====\n",
	    walltimestamp, pid, execname, (int)arg0, (int)arg1);
	printf("  argv : %S\n", curpsinfo->pr_psargs);
	ustack();
	printf("\n");
}

/* Release the fd slot on close so long-lived processes don't leak it. */
syscall::close:entry,
syscall::close_nocancel:entry
/tracked_fd[pid, (int)arg0] != 0/
{
	tracked_fd[pid, (int)arg0] = 0;
}

/* =========================================================================
 * rename / renameat / renameatx_np : the ox atomic-write path
 *
 * ox writes auth.json via temp file + rename, so the moment the file is
 * replaced corresponds to rename(tmp, auth.json). We fire on the destination
 * path matching the target.
 * ========================================================================= */

syscall::rename:entry
/arg1 != 0/
{
	self->rn_from = arg0 != 0 ? copyinstr(arg0) : "";
	self->rn_to   = copyinstr(arg1);
}

syscall::renameat:entry,
syscall::renameatx_np:entry
/arg3 != 0/
{
	self->rn_from = arg1 != 0 ? copyinstr(arg1) : "";
	self->rn_to   = copyinstr(arg3);
}

syscall::rename:entry,
syscall::renameat:entry,
syscall::renameatx_np:entry
/self->rn_to != NULL && strstr(self->rn_to, TARGET) != NULL/
{
	printf("==== RENAME %Y  pid=%d ppid=%d  %s ====\n",
	    walltimestamp, pid, ppid, execname);
	printf("  from : %s\n", self->rn_from);
	printf("  to   : %s\n", self->rn_to);
	printf("  argv : %S\n", curpsinfo->pr_psargs);
	ustack();
	printf("\n");
}

syscall::rename:entry,
syscall::renameat:entry,
syscall::renameatx_np:entry
{
	self->rn_from = 0;
	self->rn_to   = 0;
}

/* =========================================================================
 * unlink / unlinkat : outright deletion (cleanup scripts, rm, os.Remove)
 * ========================================================================= */

syscall::unlink:entry
/arg0 != 0/
{
	self->u_path = copyinstr(arg0);
}

syscall::unlink:entry
/self->u_path != NULL && strstr(self->u_path, TARGET) != NULL/
{
	printf("==== UNLINK %Y  pid=%d ppid=%d  %s ====\n",
	    walltimestamp, pid, ppid, execname);
	printf("  path : %s\n", self->u_path);
	printf("  argv : %S\n", curpsinfo->pr_psargs);
	ustack();
	printf("\n");
}

syscall::unlink:entry
{
	self->u_path = 0;
}

syscall::unlinkat:entry
/arg1 != 0/
{
	self->u_path = copyinstr(arg1);
}

syscall::unlinkat:entry
/self->u_path != NULL && strstr(self->u_path, TARGET) != NULL/
{
	printf("==== UNLINKAT %Y  pid=%d ppid=%d  %s ====\n",
	    walltimestamp, pid, ppid, execname);
	printf("  path : %s\n", self->u_path);
	printf("  argv : %S\n", curpsinfo->pr_psargs);
	ustack();
	printf("\n");
}

syscall::unlinkat:entry
{
	self->u_path = 0;
}

/* =========================================================================
 * truncate(2) by path : someone zeroing the file without reopening
 * ========================================================================= */

syscall::truncate:entry
/arg0 != 0/
{
	self->t_path = copyinstr(arg0);
	self->t_len  = arg1;
}

syscall::truncate:entry
/self->t_path != NULL && strstr(self->t_path, TARGET) != NULL/
{
	printf("==== TRUNC  %Y  pid=%d ppid=%d  %s  len=%d ====\n",
	    walltimestamp, pid, ppid, execname, (int)self->t_len);
	printf("  path : %s\n", self->t_path);
	printf("  argv : %S\n", curpsinfo->pr_psargs);
	ustack();
	printf("\n");
}

syscall::truncate:entry
{
	self->t_path = 0;
	self->t_len  = 0;
}

dtrace:::END
{
	printf("trace-auth-json: done.\n");
}
