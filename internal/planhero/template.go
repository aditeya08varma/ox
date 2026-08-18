package planhero

// posterTemplateSrc is the hero poster's SVG shell, structurally mirroring
// the reference design at .context/hero-poster-mockup.svg (dark card,
// status-colored left spine, PLAN kicker + status pill, serif title, TL;DR
// excerpt, structure stat chips, author/date + SageOx wordmark footer).
//
// This is text/template, not html/template: every dynamic value substituted
// here is pre-escaped by escapeXML in poster.go before it ever reaches
// Execute, so the template itself does no escaping. That split (escape in
// Go, substitute in the template) is deliberate — html/template's escaping
// is contextual to HTML5 parsing states, and this document is XML/SVG, not
// HTML5; running it through html/template's SVG-foreign-content handling
// would be relying on behavior that package was never written to guarantee.
const posterTemplateSrc = `<svg xmlns="http://www.w3.org/2000/svg" width="{{.Width}}" height="{{.Height}}" viewBox="0 0 {{.Width}} {{.Height}}" role="img" aria-label="Plan preview poster">
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#0d1117"/>
      <stop offset="1" stop-color="#141b24"/>
    </linearGradient>
    <linearGradient id="accent" x1="0" y1="0" x2="1" y2="0">
      <stop offset="0" stop-color="{{.SpineStop1}}"/>
      <stop offset="1" stop-color="{{.SpineStop2}}"/>
    </linearGradient>
  </defs>

  <rect width="{{.Width}}" height="{{.Height}}" fill="url(#bg)"/>
  <rect x="0" y="0" width="12" height="{{.Height}}" fill="url(#accent)"/>

  <text x="72" y="104" font-family="ui-monospace, SFMono-Regular, Menlo, monospace" font-size="26" letter-spacing="3" fill="#7d8590">PLAN</text>
  <rect x="{{.PillX}}" y="74" width="{{.PillWidth}}" height="44" rx="22" fill="{{.PillBG}}" stroke="{{.PillBorder}}" stroke-width="1.5"/>
  <circle cx="{{.PillDotX}}" cy="96" r="6" fill="{{.PillDot}}"/>
  <text x="{{.PillTextX}}" y="105" font-family="ui-sans-serif, system-ui, -apple-system, Segoe UI, sans-serif" font-size="22" font-weight="600" fill="{{.PillTextColor}}">{{.StatusLabel}}</text>

  <text x="72" y="248" font-family="Georgia, 'Times New Roman', serif" font-size="{{.TitleFontSize}}" font-weight="700" fill="#e6edf3">{{.Title}}</text>
{{- if .TLDRPresent}}
  <text x="72" y="336" font-family="ui-sans-serif, system-ui, -apple-system, Segoe UI, sans-serif" font-size="38" fill="#adbac7">{{.TLDRLine1}}</text>
{{- if .TLDRLine2}}
  <text x="72" y="392" font-family="ui-sans-serif, system-ui, -apple-system, Segoe UI, sans-serif" font-size="38" fill="#adbac7">{{.TLDRLine2}}</text>
{{- end}}
{{- end}}
{{- if .ChipsPresent}}
  <g font-family="ui-sans-serif, system-ui, sans-serif">
{{- range .Chips}}
    <rect x="{{.X}}" y="{{$.ChipsY}}" width="{{.Width}}" height="96" rx="12" fill="#161c24" stroke="#21262d"/>
    <text x="{{.TextX}}" y="{{$.ChipValueY}}" font-size="46" font-weight="700" fill="#e6edf3">{{.Value}}</text>
    <text x="{{.TextX}}" y="{{$.ChipLabelY}}" font-size="22" fill="#7d8590">{{.Label}}</text>
{{- end}}
  </g>
{{- end}}
{{- if .FooterLeftPresent}}
  <text x="72" y="648" font-family="ui-sans-serif, system-ui, sans-serif" font-size="26" fill="#7d8590">{{.FooterLeft}}</text>
{{- end}}
  <text x="{{.WordmarkX}}" y="648" text-anchor="end" font-family="ui-sans-serif, system-ui, sans-serif" font-size="26" font-weight="600" letter-spacing="1" fill="#57606a">SageOx</text>
</svg>
`
