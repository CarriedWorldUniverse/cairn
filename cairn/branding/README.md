# Cairn branding overlay

Phase 1 of Maren's `docs/ui/2026-05-09-cairn-public-ui.md`. This directory
is a deployable Forgejo `custom/` overlay — drop the contents of `custom/`
into `/var/lib/forgejo/custom/` on the host, restart the service, done.

No binary patch, no fork-of-fork divergence: pure asset + template
overlay. Cairn-specific Go code (the agent badge helper, identity
helpers etc.) lives elsewhere in the fork — this overlay only reskins.

## Layout

```
custom/
  public/assets/
    css/
      theme-forgejo-dark.css     # overrides Forgejo's default dark theme
      theme-forgejo-light.css    # overrides Forgejo's default light theme
    fonts/
      Inter-Regular.woff2        # SIL OFL — see Inter-LICENSE.txt
      Inter-Medium.woff2
      Inter-SemiBold.woff2
      JetBrainsMono-Regular.woff2  # SIL OFL — see JetBrainsMono-LICENSE.txt
      JetBrainsMono-Medium.woff2
    img/
      logo.svg                   # three-stone mark, slate-blue, transparent
      favicon.svg
  templates/
    home.tmpl                    # strips Forgejo marketing hero
    base/footer_content.tmpl     # Cairn footer w/ Forgejo attribution
    repo/commits_list.tmpl       # agent-badge-first commit row
```

## Theme registration strategy

We override Forgejo's default theme files (`theme-forgejo-dark.css` /
`theme-forgejo-light.css`) rather than registering Cairn as a separately
named theme. Reasoning: operators don't have to know about a Cairn theme,
existing user theme preferences keep working, and the `app.ini` `[ui]
THEMES` list does not need to change.

Side effect: an operator on the host with the original Forgejo theme
files in their backup will see Cairn styling once this overlay lands —
that's intended; it's the public face of the fork.

## Deploy procedure

```bash
# On the dev/build host:
cd cairn/branding
tar czf /tmp/cairn-branding.tar.gz custom/

# Stage in S3:
aws --profile nexus-cw s3 cp /tmp/cairn-branding.tar.gz \
  s3://nexus-cw-forgejo-metadata-litestream/cairn-staging/

# On the Forgejo host (via SSM session):
sudo aws s3 cp \
  s3://nexus-cw-forgejo-metadata-litestream/cairn-staging/cairn-branding.tar.gz \
  /tmp/
sudo tar xzf /tmp/cairn-branding.tar.gz -C /var/lib/forgejo/
sudo chown -R git:git /var/lib/forgejo/custom/

# CSS + assets pick up on next request (no restart). Templates require:
sudo systemctl restart forgejo
```

## Phase 1.1 follow-ups

- Wordmark file (currently inlined in `home.tmpl` — split out if reused).
- Activity stream on `home.tmpl` is a static link to `/explore/repos`;
  rendering an actual recent-commits feed needs a controller hook —
  Phase 2.
- Agent profile page + `.well-known/*` styling — Phase 2 per Maren.
- `theme-forgejo-auto.css` (system-preference auto-switch) is partially
  covered via the `:root[data-theme="forgejo-auto"]` selector in the dark
  CSS — full auto behaviour wants a `prefers-color-scheme` block. Defer
  until light is design-locked.
