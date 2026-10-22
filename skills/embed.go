package skillsbundle

import "embed"

// FS contains the bundled c2j skills and trusted source allowlist used by c2j init and c2j skills update.
//
//go:embed c2j-*/* c2j-*/*/* c2ops-*/* c2ops-*/*/* sources.yaml
var FS embed.FS
