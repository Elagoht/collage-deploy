// A collage plugin that turns a static build's captured headers and redirects into
// the configuration files a static host reads: _headers and _redirects for Netlify
// and Cloudflare Pages, vercel.json for Vercel, and the redirect pages
// GitHub Pages can serve.
//
// It requires collage the way any consumer does, and reaches nothing the framework
// does not offer every plugin.
module github.com/Elagoht/collage-deploy

go 1.26

require (
	github.com/Elagoht/collage v0.52.0
	github.com/Elagoht/collage-redirects v0.2.0
	github.com/Elagoht/collage-secure v0.2.3
)
