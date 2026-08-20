import {
  Box,
  Button,
  IconButton,
  Popover,
  Stack,
  TextField,
  Tooltip,
} from "@mui/material";
import PaletteOutlinedIcon from "@mui/icons-material/PaletteOutlined";
import { useEffect, useRef, useState } from "react";
import { isHexColor } from "../theme";
import { useAccent } from "../theme/AppThemeProvider";

// A handful of curated presets so the user can pick a tasteful color in one
// click. They're roughly the Tailwind "500" values for vivid hues.
const PRESETS = [
  "#3b82f6", // blue (default)
  "#7c83ff", // indigo
  "#00ffff", // cyan
  "#22c55e", // green
  "#f59e0b", // amber
  "#ef4444", // red
  "#ec4899", // pink
  "#a855f7", // purple
  "#14b8a6", // teal
];

export function ColorPickerButton() {
  const { accent, setAccent, resetAccent } = useAccent();
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const [draftHex, setDraftHex] = useState(accent);
  const colorInputRef = useRef<HTMLInputElement | null>(null);

  // Keep the text input in sync when the accent changes (e.g. preset clicked
  // or another tab updated localStorage).
  useEffect(() => setDraftHex(accent), [accent]);

  const open = Boolean(anchor);

  const apply = (hex: string) => {
    if (isHexColor(hex)) setAccent(hex);
  };

  return (
    <>
      <IconButton
        aria-label="Choose theme color"
        onClick={(e) => setAnchor(e.currentTarget)}
        size="medium"
        sx={{
          color: "text.primary",
          borderRadius: 2,
          width: 40,
          height: 40,
          "&:hover": { backgroundColor: "action.hover" },
        }}
      >
        <Box
          sx={{
            position: "relative",
            width: 22,
            height: 22,
            display: "grid",
            placeItems: "center",
          }}
        >
          <PaletteOutlinedIcon fontSize="small" />
          <Box
            sx={{
              position: "absolute",
              bottom: -2,
              right: -2,
              width: 8,
              height: 8,
              borderRadius: "50%",
              bgcolor: accent,
              // Theme-aware ring around the accent dot so it reads
              // cleanly against either the dark or light topbar.
              border: "1.5px solid",
              borderColor: "background.default",
            }}
          />
        </Box>
      </IconButton>
      <Popover
        open={open}
        anchorEl={anchor}
        onClose={() => setAnchor(null)}
        anchorOrigin={{ vertical: "bottom", horizontal: "right" }}
        transformOrigin={{ vertical: "top", horizontal: "right" }}
        slotProps={{ paper: { sx: { p: 2, width: 260 } } }}
      >
        <Stack spacing={1.5}>
          <Box
            sx={{
              display: "grid",
              gridTemplateColumns: "repeat(5, 1fr)",
              gap: 1,
            }}
          >
            {PRESETS.map((c, i) => (
              <Swatch
                key={c}
                color={c}
                index={i}
                selected={c.toLowerCase() === accent.toLowerCase()}
                onPick={() => apply(c)}
              />
            ))}
          </Box>
          <Stack direction="row" spacing={1} alignItems="center">
            <Box
              onClick={() => colorInputRef.current?.click()}
              sx={{
                width: 36,
                height: 36,
                flexShrink: 0,
                borderRadius: 1.5,
                bgcolor: accent,
                cursor: "pointer",
                outline: "1px solid",
                outlineColor: "divider",
                transition:
                  "background-color 0.32s cubic-bezier(0.4,0,0.2,1), transform 0.12s cubic-bezier(0.4,0,0.2,1)",
                "&:hover": { transform: "scale(1.04)" },
                "&:active": { transform: "scale(0.94)" },
              }}
            />
            <input
              ref={colorInputRef}
              type="color"
              value={accent}
              onChange={(e) => apply(e.target.value)}
              style={{ display: "none" }}
            />
            <TextField
              size="small"
              fullWidth
              value={draftHex}
              onChange={(e) => {
                const v = e.target.value;
                setDraftHex(v);
                if (isHexColor(v)) setAccent(v);
              }}
              placeholder="#7c83ff"
              inputProps={{ maxLength: 7, "aria-label": "Custom hex color" }}
              error={draftHex.length > 0 && !isHexColor(draftHex)}
            />
          </Stack>
          <Button
            size="small"
            variant="outlined"
            color="inherit"
            onClick={() => {
              resetAccent();
              setAnchor(null);
            }}
          >
            Reset to default
          </Button>
        </Stack>
      </Popover>
    </>
  );
}

// Swatch — a single colour cell. The entry fade/grow animation still plays
// when the popover opens, but picking a colour no longer triggers an extra
// pop bounce: that second animation used `composite: "add"` layered on top
// of the CSS hover/active transform transitions, which caused a visible
// twitch as the WAAPI effect finished and the element snapped back to its
// hover-scaled baseline.
function Swatch({
  color,
  index,
  selected,
  onPick,
}: {
  color: string;
  index: number;
  selected: boolean;
  onPick: () => void;
}) {
  return (
    <Tooltip title={color.toUpperCase()}>
      <Box
        role="button"
        tabIndex={0}
        onClick={onPick}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") onPick();
        }}
        sx={{
          position: "relative",
          width: 36,
          height: 36,
          borderRadius: 1.5,
          bgcolor: color,
          cursor: "pointer",
          outline: selected ? "2px solid" : "1px solid",
          outlineColor: selected ? "text.primary" : "divider",
          outlineOffset: selected ? 2 : 0,
          zIndex: 1,
          transition:
            "transform 0.18s cubic-bezier(0.4,0,0.2,1), outline-offset 0.18s cubic-bezier(0.4,0,0.2,1), box-shadow 0.18s cubic-bezier(0.4,0,0.2,1), outline-color 0.2s cubic-bezier(0.4,0,0.2,1)",
          animation: `sb-swatch-enter 0.32s ${index * 32}ms cubic-bezier(0.34, 1.4, 0.64, 1) backwards`,
          "@keyframes sb-swatch-enter": {
            from: { opacity: 0, transform: "scale(0.6)" },
            to: { opacity: 1, transform: "scale(1)" },
          },
          "&:hover": {
            transform: "scale(1.08)",
            zIndex: 5,
            boxShadow: `0 0 0 4px color-mix(in srgb, ${color} 22%, transparent)`,
          },
          "&:active": {
            transform: "scale(0.92)",
            transition:
              "transform 0.08s cubic-bezier(0.4,0,0.2,1), outline-offset 0.18s cubic-bezier(0.4,0,0.2,1)",
          },
          "&:focus-visible": {
            outline: "2px solid",
            outlineColor: "text.primary",
            outlineOffset: 2,
          },
        }}
      />
    </Tooltip>
  );
}
