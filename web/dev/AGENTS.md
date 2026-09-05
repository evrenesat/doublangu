# Local reader design fixtures

These fixtures are synthetic, development-only inputs for the real reader route.
Do not add authentication bypasses, remote credentials, provider calls, or fake
successful writes. Vite serves them only when DOUBLANGU_READER_DEMO=1. Production
builds must not register this middleware or bundle the sample article.
