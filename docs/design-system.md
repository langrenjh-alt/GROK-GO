# GROK-GO Design System

## Principles

GROK-GO is an operational console. Interfaces prioritize scanning, comparison, repeated action, and unambiguous system state. The implementation follows Vercel Geist's token model and interaction guidance without depending on Vercel's private `@vercel/geistcn` packages.

- Keep page structure quiet and dense. Use tables and full-width bands for operational data.
- Use cards only for individual repeated objects, dialogs, or genuinely framed tools. Do not nest cards.
- Use color to reinforce status, never as the only status signal.
- Prefer explicit nouns and `Verb + Noun` commands. Destructive confirmations name the affected resource.
- Every page must provide loading, empty, error, and populated states.

Reference documentation:

- [Geist introduction](https://vercel.com/geist/introduction)
- [Colors](https://vercel.com/geist/colors)
- [Typography](https://vercel.com/geist/typography)
- [Materials](https://vercel.com/geist/materials)
- [Button](https://vercel.com/geist/button)
- [Input](https://vercel.com/geist/input)
- [Table](https://vercel.com/geist/table)
- [Tabs](https://vercel.com/geist/tabs)
- [Modal](https://vercel.com/geist/modal)

## Foundations

### Color

Primitive tokens use Geist's 100-1000 scale. Component backgrounds use 100-300, borders use 400-600, high-contrast fills use 700-800, and text/icons use 900-1000. Components consume semantic aliases instead of primitive values:

| Role | CSS token | Typical use |
| --- | --- | --- |
| Page | `--color-page` | App canvas |
| Surface | `--color-surface` | Sidebar, table, modal |
| Subtle | `--color-subtle` | Hover and selected rows |
| Primary foreground | `--color-fg` | Headings and primary text |
| Muted foreground | `--color-fg-muted` | Body copy and labels |
| Border | `--color-border` | Dividers and controls |
| Accent | `--color-blue-700` | Primary focus and progress |
| Success / warning / danger | `--color-green`, `--color-amber`, `--color-danger` | Status with text or icon |

Light, dark, and system themes are managed by `next-themes`. Theme changes only remap tokens; component markup stays identical. New feature colors must define both light and dark values and meet WCAG contrast.

### Typography

Use the official `geist` package. Geist Sans is the interface font and Geist Mono is reserved for IDs, API paths, keys, numeric comparisons, logs, and code.

- Page heading: `text-heading-24`, increasing to the fixed `text-heading-32` size on wider screens.
- Section heading: `text-heading-16` or `text-heading-20`.
- Controls and single-line labels: `text-label-13` / `text-label-14`.
- Body copy: `text-copy-13` / `text-copy-14`.
- Numeric columns use `tabular-nums`.
- Font sizes never scale continuously with viewport width; use discrete breakpoints.

### Spacing, Shape, and Elevation

The base spacing unit is 4px. Common values are 8, 12, 16, 24, 32, 40, and 64px. Desktop page padding is 24-32px; mobile page padding is 16px.

- Small, medium, and large controls are 32, 36, and 40px high.
- Resting controls and panels use a 6px radius.
- Menus and dialogs use a 12px radius; fullscreen takeovers may use 16px.
- Elevation is semantic: `shadow-control`, `shadow-menu`, `shadow-modal`, and `shadow-tooltip`.
- Dividers and surface changes should carry most hierarchy. Avoid gratuitous shadows.

### Layout

The desktop shell uses a fixed 236px sidebar and a 53px top bar. The content column remains fluid. Below the `lg` breakpoint the sidebar becomes a focus-managed modal drawer. Tables scroll horizontally on narrow screens rather than crushing labels or controls.

## Components

Local components live in `web/src/components/ui`. Feature code must use them instead of restyling raw controls.

### Buttons

`Button` supports `primary`, `secondary`, `tertiary`, and `danger`, plus small/medium/large sizes and an announced loading state. Use buttons for mutations and links for navigation. `IconButton` requires a `label`; visible-text buttons must not add a competing `aria-label`.

### Forms

`Input`, `Select`, `Textarea`, and `Switch` own their label, description, validation association, focus ring, and disabled state. Validate text fields on blur or submit. Placeholders show examples, not instructions. Errors name the field and constraint and end with a period.

### Badges and Status

`Badge` uses a restrained neutral, blue, green, amber, or red tone. Status badges pair a dot with text. Do not represent status through color alone. Keep labels concise and stable across pages.

### Tables

`DataTable` is based on TanStack Table and renders semantic table markup. Sortable headers are buttons and expose `aria-sort`. Unknown values render `-`. Empty collections render an `EmptyState` outside the table. IDs, durations, quotas, and token counts use mono/tabular formatting.

### Dialogs and Toasts

Radix primitives provide focus trap, Escape behavior, outside-click handling, and trigger focus restoration. Destructive dialogs initially focus Cancel and require a typed resource name for high-impact actions. Toasts acknowledge terminal outcomes; field validation remains inline. Toast copy is sentence case and short.

### Code Editor

The debugger uses CodeMirror with JSON syntax support. Request and response editors have stable heights, visible labels, read-only response state, and a separate copy action. Parse errors appear inline before a request is sent.

## Accessibility

The target is WCAG 2.2 AA.

- Text contrast is at least 4.5:1; controls, focus indicators, and meaningful graphics are at least 3:1.
- Every interactive element has a visible `focus-visible` ring.
- Modal focus is trapped and restored; tabs retain arrow-key and Enter/Space behavior.
- Inputs always have programmatic labels. Helper and error text use `aria-describedby`.
- Live acknowledgements are polite; blocking errors use an alert role.
- Motion respects `prefers-reduced-motion`.
- Desktop controls may be 32px high; mobile layouts provide at least 44px separation or target area for primary touch actions.
- English and Simplified Chinese layouts must be checked for clipping and truncation.

## Engineering Contract

- Frontend: Next.js App Router, React, TypeScript, Tailwind CSS, static export.
- Production output: `web/out`; no Server Actions, route handlers, or Next server runtime features.
- API: same-origin `/admin/api`, cookie credentials, JSON envelopes supported.
- Icons: Lucide React. Do not add hand-drawn SVG controls.
- Interactive primitives: Radix UI. Data tables: TanStack Table. Theme: `next-themes`.
- Unit tests use Vitest, Testing Library, and axe-core. Browser coverage uses Playwright at desktop and mobile widths.
