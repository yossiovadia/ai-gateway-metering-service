package dashboard

import "embed"

//go:embed dashboard.html admin.html routing.html compression.html myaccount.html pipeline.html
var FS embed.FS
