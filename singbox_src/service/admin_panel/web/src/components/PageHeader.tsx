import { Box, Stack, Typography } from "@mui/material";
import { type ReactNode } from "react";
import { useAccent } from "../theme/AppThemeProvider";

export interface PageHeaderProps {
  title: string;
  subtitle?: string;
  icon?: ReactNode;
  actions?: ReactNode;
}

// PageHeader is the visual top of every page: an accent-colored icon badge,
// a title, an optional subtitle, and a right-aligned actions slot. A thin
// hairline below visually separates the header from the page content.
//
// Responsive behaviour: on viewports ≥ sm the classic row layout stays
// (icon + title on the left, actions on the right). On xs viewports the
// actions wrap below the title row so the toolbar never pushes the
// title off-screen on a narrow phone.
//
// HEIGHT is explicitly locked. The header used to size to its content
// (icon 46 + pt + pb), which let any small content shift — a font
// metrics swap when Inter finishes loading, a transient inline `height:
// Xpx` from CrudPage's WAAPI tween, an actions row briefly wrapping —
// reflow the entire flex column it sits in and visibly slide the
// topbar / page title up by a few pixels right as fetched rows arrive.
// Pinning a `height` (matched by `min/maxHeight` for belt-and-braces)
// plus `flexShrink: 0` makes the box a fixed-size brick: nothing inside
// or above can change its outer height regardless of state. Tuned at
// 75 px so the existing `pt: 1` + 46 px icon + `pb: 2.5` + 1 px hairline
// fits the inside of the box without trimming.
export const PAGE_HEADER_HEIGHT = 75;
export function PageHeader({ title, subtitle, icon, actions }: PageHeaderProps) {
  const { palette } = useAccent();
  return (
    <Box
      sx={{
        mb: 3,
        pt: 1,
        pb: 2.5,
        height: PAGE_HEADER_HEIGHT,
        minHeight: PAGE_HEADER_HEIGHT,
        maxHeight: PAGE_HEADER_HEIGHT,
        flexShrink: 0,
        boxSizing: "border-box",
        borderBottom: `1px solid ${palette.border}`,
      }}
    >
      <Stack
        // Always keep title and actions on the same row, with the
        // actions pinned to the right edge — even on phones. The
        // previous `xs: column` stack pushed the action toolbar
        // below the title, which made the icon-only Filters/Refresh/
        // New buttons appear left-aligned under the heading instead
        // of in their familiar top-right corner.
        direction="row"
        alignItems="center"
        spacing={2}
      >
        <Stack
          direction="row"
          alignItems="center"
          spacing={2}
          sx={{ flexGrow: 1, minWidth: 0 }}
        >
          {icon && (
            <Box
              sx={{
                width: 46,
                height: 46,
                display: "grid",
                placeItems: "center",
                borderRadius: 2.5,
                flexShrink: 0,
                // CSS-variable accent so the badge smoothly crossfades to a
                // new colour when the user picks a different accent. Using a
                // literal `palette.accent` here would cause the className to
                // change on every accent update and cancel any transition.
                bgcolor:
                  "color-mix(in srgb, var(--sb-accent) 14%, transparent)",
                color: "var(--sb-accent)",
                border:
                  "1px solid color-mix(in srgb, var(--sb-accent) 32%, transparent)",
                // Stacked box-shadows so the halo is the sum of several
                // overlapping soft drops at decreasing alpha — that
                // gives Chromium enough intermediate samples to land
                // between to avoid the visible step bands a single
                // `0 6px 18px @ 18%` shadow produced on the dark
                // background (the alpha channel quantises to ~256
                // levels, and a single low-alpha shadow doesn't have
                // enough headroom across the blur radius to dither
                // cleanly).
                boxShadow: [
                  "inset 0 0 0 1px color-mix(in srgb, var(--sb-accent) 8%, transparent)",
                  "0 1px 2px color-mix(in srgb, var(--sb-accent) 26%, transparent)",
                  "0 3px 6px color-mix(in srgb, var(--sb-accent) 18%, transparent)",
                  "0 6px 14px color-mix(in srgb, var(--sb-accent) 12%, transparent)",
                  "0 10px 28px color-mix(in srgb, var(--sb-accent) 7%, transparent)",
                ].join(", "),
                transition:
                  "background-color 0.32s cubic-bezier(0.4,0,0.2,1), color 0.32s cubic-bezier(0.4,0,0.2,1), border-color 0.32s cubic-bezier(0.4,0,0.2,1), box-shadow 0.32s cubic-bezier(0.4,0,0.2,1)",
              }}
            >
              {icon}
            </Box>
          )}
          <Box sx={{ flexGrow: 1, minWidth: 0 }}>
            <Typography
              variant="h5"
              sx={{
                lineHeight: 1.15,
                // Slightly smaller heading on xs so long page titles
                // like "Bandwidth limiters" fit the narrower column.
                fontSize: { xs: 19, sm: 22 },
                fontWeight: 600,
                letterSpacing: -0.4,
              }}
            >
              {title}
            </Typography>
            {subtitle && (
              <Typography
                variant="body2"
                color="text.secondary"
                sx={{
                  mt: 0.5,
                  fontSize: { xs: 12.5, sm: 13.5 },
                  // Keep the subtitle readable on narrow screens without
                  // forcing an overflow ellipsis — wrapping is preferable
                  // to truncation for description text.
                  whiteSpace: "normal",
                }}
              >
                {subtitle}
              </Typography>
            )}
          </Box>
        </Stack>
        {actions && (
          <Box
            sx={{
              flexShrink: 0,
              display: "flex",
              alignItems: "center",
              // Always pinned to the right of the row regardless of
              // viewport width.
              justifyContent: "flex-end",
              flexWrap: "wrap",
              rowGap: 1,
            }}
          >
            {actions}
          </Box>
        )}
      </Stack>
    </Box>
  );
}
