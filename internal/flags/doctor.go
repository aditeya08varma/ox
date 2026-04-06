package flags

import (
	"context"

	"github.com/sageox/ox/internal/doctorapi"
)

// DoctorProvider maps server-returned doctorapi.Features onto flag fields.
// The Features value must be passed in — this provider never makes network
// calls. Pass nil Features to make the provider inactive (returns nil patch).
//
// Covers account/team state flags (Waitlist, Stealth, Temporal, OCR) that
// come from /api/v1/cli/doctor/context. Once /api/v1/cli/settings is live,
// these fields will migrate there and this provider can be retired.
type DoctorProvider struct {
	Features *doctorapi.Features
}

func (p DoctorProvider) Patch(_ context.Context) (*Patch, Source, error) {
	if p.Features == nil {
		return nil, SourceDoctor, nil
	}
	return &Patch{
		Waitlist: boolPtr(p.Features.Waitlist),
		Stealth:  boolPtr(p.Features.Stealth),
		Temporal: boolPtr(p.Features.Temporal),
		OCR:      boolPtr(p.Features.OCR),
	}, SourceDoctor, nil
}
