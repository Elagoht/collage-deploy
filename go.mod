// A collage plugin that turns a static build's captured headers and redirects into
// the configuration files a static host reads: _headers and _redirects for Netlify
// and Cloudflare Pages, vercel.json for Vercel, and the 404 and redirect pages
// GitHub Pages can do.
//
// It requires collage the way any consumer does, and reaches nothing the framework
// does not offer every plugin.
module github.com/Elagoht/collage-deploy

go 1.26

require github.com/Elagoht/collage v0.51.2

// Until collage v0.52.0 is tagged, the build hook's redirects and headers come
// from the unreleased deploy worktree.
replace github.com/Elagoht/collage => ../collage-deploy-core
