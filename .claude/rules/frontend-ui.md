---
paths:
  - "web/frontend/**"
---

# Frontend UI rules (Sentinel web UI)

Style: soft, airy dashboard. Pale blue-grey page, white rounded cards with soft shadows, royal-blue primary,
navy text, teal accent, pill status badges, smooth gradient charts, dark navy chart tooltips, sidebar navigation.

- **Tokens only.** Colours, radius, shadows and fonts come from `src/index.css` (`bg-card`, `text-muted-foreground`,
  `text-primary`, `bg-success-soft`, `shadow-card`, `shadow-float`, `rounded-2xl`...). No hex/rgb/oklch literals in components;
  need a new colour? add a token for both `:root` and `.dark` first.
- **shadcn components: CLI first, then restyle.** Add with `pnpm dlx shadcn@latest add <name>` (style `aria-rhea`,
  react-aria-components, so `onPress`, not `onClick`), then edit the generated file in `src/components/ui/` to fit the
  tokens. Do not hand-write a component that shadcn already provides. After adding, run `npx tsc -b` (generated files
  sometimes have unused imports).
- **Reuse layout parts** from `src/components/page.tsx` (`PageHeader`, `StatStrip`, `Panel`, `Flash`, `Empty`) and status
  pills from `src/components/status.tsx`; do not re-create them per page.
- **Data**: all calls go through `api()` in `src/lib/api.ts` (JSON, snake_case, CSRF header) with TanStack Query; types live in
  `src/lib/types.ts` and mirror `internal/server/webapi*.go`. Poll with `refetchInterval`, not websockets.
- **Monitoring data** (CPU, memory, containers, S.M.A.R.T...) comes from Beszel through Sentinel's `/api/ui/*`; the UI never calls Beszel. TS types in `lib/types.ts` use the same field names as `internal/beszel` JSON: do not rename or re-map them.
- **CSP**: the server sends a strict Content-Security-Policy. No inline `<script>`, no remote scripts, fonts or images
  (bundle everything via npm); inline styles are allowed.
- **Security**: never store tokens or server data in `localStorage` or URLs; one-time agent tokens travel in router state only.
- **Copy** is English. No emoji. Status is rule-based (thresholds, schedules): do not add AI/LLM features.
- **Done means**: `make test` is green and the page was checked in a browser in light and dark mode at ~1440px and ~390px.
