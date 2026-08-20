import { Box, IconButton, Tooltip } from "@mui/material";
import LightModeIcon from "@mui/icons-material/LightMode";
import DarkModeIcon from "@mui/icons-material/DarkMode";
import { useAccent } from "../theme/AppThemeProvider";
import { ColorPickerButton } from "./ColorPickerButton";

// Small toolbar pinned to the top-right of the login page. It
// surfaces the same appearance controls the main Layout exposes in
// its top bar — theme mode toggle (light ↔ dark) and the accent
// colour picker — so users can configure the UI's look without
// having to sign in first.
//
// Style-wise it uses a frosted-pill treatment that floats above the
// animated backdrop, matching the visual language the rest of the
// login page uses.

export function LoginThemeControls() {
  const { mode, toggleMode } = useAccent();
  return (
    <Box
      sx={{
        position: "absolute",
        top: { xs: 12, sm: 20 },
        right: { xs: 12, sm: 20 },
        // Above the animated backdrop (auto z-index) and at the same
        // level as the auth form card so the pill is always
        // clickable no matter how the viewport stacks.
        zIndex: 2,
        display: "flex",
        alignItems: "center",
        gap: 0.5,
        p: 0.5,
        borderRadius: 999,
        bgcolor: (t) =>
          t.palette.mode === "light"
            ? "rgba(255,255,255,0.72)"
            : "rgba(20,20,20,0.55)",
        backdropFilter: "blur(8px)",
        WebkitBackdropFilter: "blur(8px)",
        border: (t) => `1px solid ${t.palette.divider}`,
        boxShadow: (t) =>
          t.palette.mode === "light"
            ? "0 6px 18px rgba(15,23,42,0.10)"
            : "0 6px 18px rgba(0,0,0,0.45)",
        // Normalise every IconButton inside the pill (including the
        // one the ColorPickerButton component renders for its palette
        // popover trigger) to a compact 34 px round button. Without
        // this override the ColorPickerButton's default-medium
        // IconButton would sit a few pixels taller than the theme
        // toggle, making the pill look lopsided.
        "& .MuiIconButton-root": {
          width: 34,
          height: 34,
          borderRadius: 999,
          transition:
            "color 0.2s cubic-bezier(0.4,0,0.2,1), background-color 0.2s cubic-bezier(0.4,0,0.2,1)",
          "&:hover": { color: "var(--sb-accent)" },
        },
      }}
    >
      <Tooltip
        title={
          mode === "light" ? "Switch to dark theme" : "Switch to light theme"
        }
        placement="top"
      >
        <IconButton
          // Passing the click event along so the theme change's
          // View Transitions API circular reveal radiates from the
          // button's exact click point, not the screen centre.
          onClick={(e) => toggleMode(e)}
          aria-label="Toggle color theme"
          sx={{ color: "text.secondary" }}
        >
          {mode === "light" ? (
            <DarkModeIcon fontSize="small" />
          ) : (
            <LightModeIcon fontSize="small" />
          )}
        </IconButton>
      </Tooltip>
      <ColorPickerButton />
    </Box>
  );
}
