---
name: netsub store
description: Source-evidenced storefront tokens and tier colors.
colors:
  background: "#333333"
  surface: "#3d3d3d"
  line: "#555555"
  text: "#e8eef2"
  muted: "#bdc2ca"
  accent: "#edc989"
  rare: "#9dbbc9"
  unique: "#bfafd6"
  error: "#efb9a9"
typography:
  headline:
    fontFamily: "JetBrains, monospace"
    fontSize: "28px"
    fontWeight: 400
    lineHeight: 1.3
    letterSpacing: "-0.03em"
  title:
    fontFamily: "JetBrains, monospace"
    fontSize: "18px"
    fontWeight: 400
    lineHeight: 1.3
    letterSpacing: "-0.02em"
  body:
    fontFamily: "JetBrains, monospace"
    fontSize: "13px"
    lineHeight: 1.6
  label:
    fontFamily: "JetBrains, monospace"
    fontSize: "12px"
  tier-label:
    fontFamily: "JetBrains, monospace"
    fontSize: "11px"
rounded:
  control: "4px"
  modal: "8px"
spacing:
  small: "8px"
  compact: "12px"
  regular: "16px"
  medium: "20px"
  section: "24px"
  large: "32px"
  wide: "40px"
components:
  button:
    textColor: "{colors.text}"
    rounded: "{rounded.control}"
    padding: "10px 16px"
  button-primary:
    backgroundColor: "{colors.text}"
    textColor: "{colors.background}"
    rounded: "{rounded.control}"
    padding: "10px 16px"
  input:
    backgroundColor: "{colors.background}"
    textColor: "{colors.text}"
    rounded: "{rounded.control}"
    padding: "10px 12px"
  tier-label:
    typography: "{typography.tier-label}"
  planet:
    textColor: "{colors.text}"
    padding: "30px 8px 24px"
---

# Design System: netsub store

## Colors

The storefront uses charcoal surfaces, light text, and muted tier accents. Tokens reflect `web/src/style.css`; tier assignments also appear in `web/src/main.jsx`.

### Primary

- **Warm gold** (`accent`): Premium sigils, Premium filters and tier labels; also the brand accent, focus outline, links on hover, and text selection.

### Secondary

- **Muted blue** (`rare`): Rare sigils, filters, and tier labels.
- **Lavender** (`unique`): Unique sigils, filters, and tier labels.

### Neutral

- **Charcoal** (`background`): Page, inputs, and resting sigil backgrounds.
- **Raised charcoal** (`surface`): Hovered and selected catalog items.
- **Divider gray** (`line`): Control outlines, separators, and catalog row borders.
- **Light text** (`text`): Main copy, Standard sigils, and primary button backgrounds.
- **Muted text** (`muted`): Supporting copy, prices, and inactive controls.

Errors use the `error` token.

**The Tier Identity Rule.** Rare uses blue, Unique uses lavender, and Premium uses gold consistently across sigils, filters, and labels. Premium name-and-sigil items share Premium gold. Visible text identifies the tier alongside color.

## Typography

The bundled JetBrains face supplies headings, body copy, labels, and controls, with a monospace fallback. Headings remain regular weight. The hierarchy uses headline, title, body, and label tokens; smaller tier labels use (11px), and tertiary headings use (14px). Supporting captions have line height (1.65). Paragraphs have a maximum width of (70ch).

At the mobile breakpoint, headlines use (24px) and text inputs and selects use (16px).

## Layout

Header, main content, and footer share a centered maximum width of (1248px) and horizontal padding of (40px). Padding becomes (24px) at (1000px) and (20px) at (720px).

The catalog has four columns, or three beside a selected item's (304px) checkout. At (1000px), these become three or two columns, with a (280px) checkout. At (720px), catalog items use two columns and checkout appears above them in the same column. Spacing tokens capture the recurring gaps and section padding.

## Elevation & Depth

Surfaces use tonal changes and thin dividers. Selected catalog items share the hover surface and have a gold bottom border. Active filters use a light inset underline. The modal uses a dark backdrop; floating drop shadows are absent from the current stylesheet.

## Shapes

Controls have small rounded corners; modal corners use the modal radius. Catalog items remain square and use a bottom divider. Buttons, text fields, and selects have a minimum height of (44px).

## Components

### Buttons

Default buttons use transparent backgrounds and thin outlines; hover applies the surface color and muted outline. Primary buttons use light text color as their fill and charcoal text, with a white hover fill. Disabled buttons use opacity (0.45). Background and border transitions take (160ms) with ease-out timing.

### Inputs and navigation

Inputs and selects use charcoal backgrounds and outlined control shapes. Navigation uses compact labels with a minimum link height of (44px). Header navigation links are hidden at the mobile breakpoint, while the wallet control remains visible.

### Tier filters and catalog items

Tier filters use text buttons with an active underline. Catalog items stack the sigil, name, price, and optional tier label. Sigil backgrounds follow the item's resting, hover, or selected surface. Catalog items have no outer card outline.

### Brand and payment marks

The header wordmark reads `networked subject_`, with the trailing underscore in the brand accent. Checkout shows a row of (24px) white circular currency marks below its heading, with (10px) gaps. Marks reflect configured payment availability and have accessible currency names. Bitcoin, Ethereum, USDC, and Monero marks come from the CC0 cryptocurrency-icons set; the Nockchain mark comes from the project’s official site.

### Focus and motion

Buttons, links, fields, selects, and disclosure summaries use a gold focus outline (2px), offset by (4px). Reduced-motion preferences disable transitions and smooth scrolling.

## Do's and Don'ts

### Do:

- **Do** preserve tier color assignments across sigils, filters, and labels.
- **Do** retain visible tier labels and active filter underlines alongside color.
- **Do** let sigil backgrounds match the containing item's current surface.

### Don't:

- **Don't** assign a separate color to Premium name-and-sigil items; they share Premium gold.
- **Don't** place commercial prices or payment availability in visual tokens; checkout reads configuration.
