Pull request: docs(cairn): runbook — explicit build procedure (§0.1)

Description:
Lessons from the 2026-05-10 deploy (Plan 7 Stage 2):

1. make frontend MUST run before go generate -tags bindata, otherwise
   public/ is empty and the binary's embedded assets are empty too —
   runtime serves 404 on /assets/js/index.js etc. Recovered by tarring
   public/ from a local re-build and extracting to
   /var/lib/forgejo/custom/public/ on the host.

2. -target x86_64-linux-gnu under zig produces a binary that LOOKS
   static but has PT_INTERP set — actual static linking requires
   -target x86_64-linux-musl. With the netgo build tag, DNS works
   fine on Amazon Linux 2023.

3. go generate -tags bindata is required before go build with the
   bindata tag, otherwise compile fails with "undefined: Assets".

Adds §0.1 to the runbook with the canonical 3-step build sequence
and a 'Common failures and fixes' subsection.

Branch: cairn-runbook-build-procedure -> cairn

Commits:
- docs(cairn): runbook — explicit build procedure (§0.1)

Files changed:
- cairn/deploy/deployment-runbook.md

Diff:
```diff
--- a/cairn/deploy/deployment-runbook.md
+++ b/cairn/deploy/deployment-runbook.md
@@ -27,6 +27,46 @@
 If any check fails, stop and surface to the operator before continuing.
 
+### 0.1 Build the binary (off-host)
+
+Three steps in this order — getting it wrong silently produces a binary missing assets or migration data.
+
+\`\`\`bash
+cd ~/Source/cairn
+
+# Step 1 — frontend assets (webpack build into public/)
+make frontend
+
+# Step 2 — bindata regeneration
+go generate -tags 'bindata sqlite sqlite_unlock_notify' ./...
+
+# Step 3 — cross-compile via zig
+ZIG=~/Source/.tools/zig-macos-aarch64-0.14.0/zig
+CC="$ZIG cc -target x86_64-linux-musl" \\
+CXX="$ZIG c++ -target x86_64-linux-musl" \\
+GOOS=linux GOARCH=amd64 CGO_ENABLED=1 \\
+go build \\
+    -tags 'bindata timetzdata sqlite sqlite_unlock_notify netgo osusergo' \\
+    -ldflags '-linkmode external -extldflags "-static"' \\
+    -trimpath \\
+    -o /tmp/cairn-build/forgejo \\
+    .
+\`\`\`
+
+**Common failures and fixes:**
+- \`make frontend\` skipped → runtime 404 on \`/assets/js/index.js\`
+- \`go generate\` skipped → compile error \`undefined: Assets\`
+- \`-target x86_64-linux-gnu\` → silently dynamic-linked despite \`-static\`
+
 ---
 
 ## 1. Phase 0 — Pre-flight inventory
```
