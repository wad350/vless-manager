import { Box, IconButton, Tooltip } from "@mui/material";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import CheckIcon from "@mui/icons-material/Check";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
} from "react";

// copyToClipboard writes `text` to the OS clipboard, preferring the modern
// async Clipboard API and falling back to a hidden textarea + execCommand
// for non-secure contexts (HTTP, older browsers) where `navigator.clipboard`
// is undefined.
async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (
      typeof navigator !== "undefined" &&
      navigator.clipboard &&
      typeof navigator.clipboard.writeText === "function"
    ) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through to legacy path */
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.top = "0";
    ta.style.left = "0";
    ta.style.opacity = "0";
    ta.style.pointerEvents = "none";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

interface CopyableIdProps {
  // The string written to the clipboard. Also rendered inline as the
  // visible label — the caller doesn't pass it twice.
  value: string;
  // Optional override for the tooltip's idle title ("Copy UUID" by default).
  // Used to keep the hint accurate when the same component is reused for
  // non-UUID identifiers (numeric IDs, IPs, etc.).
  label?: string;
}

// CopyableId — inline value + click-to-copy with a small always-visible
// icon and a brief "Copied!" confirmation. Designed to drop into table
// cells styled by `ID_CELL_SX`: long values clip with an ellipsis on the
// left side, while the icon stays pinned to the right.
//
// The whole component is one click target: clicking either the text or
// the explicit icon button copies the value, so users don't have to aim
// for the tiny icon. After a successful copy the icon flips to a green
// checkmark for ~1.2 s so users get visual confirmation regardless of
// the cursor position.
export function CopyableId({ value, label }: CopyableIdProps) {
  const [copied, setCopied] = useState(false);
  const timeoutRef = useRef<number | null>(null);

  // Cancel any pending "Copied!" reset on unmount so a fast row remount
  // (pagination, filter apply) doesn't run setState on a dead component.
  useEffect(
    () => () => {
      if (timeoutRef.current !== null) {
        window.clearTimeout(timeoutRef.current);
      }
    },
    [],
  );

  const handleCopy = useCallback(
    async (e?: ReactMouseEvent<HTMLElement>) => {
      // Stop the click from bubbling to ancestors that might react to row
      // clicks (selection toggles, navigation), and from triggering text
      // selection on double-click.
      e?.stopPropagation();
      e?.preventDefault();
      const ok = await copyToClipboard(value);
      if (!ok) return;
      setCopied(true);
      if (timeoutRef.current !== null) {
        window.clearTimeout(timeoutRef.current);
      }
      timeoutRef.current = window.setTimeout(() => {
        setCopied(false);
        timeoutRef.current = null;
      }, 1200);
    },
    [value],
  );

  const idleTitle = label ?? "Copy UUID";

  return (
    <Box
      onClick={handleCopy}
      role="button"
      aria-label={idleTitle}
      sx={{
        display: "inline-flex",
        alignItems: "center",
        gap: 0.5,
        maxWidth: "100%",
        cursor: "pointer",
        userSelect: "text",
        // Subtle hover affordance — the cell text shifts toward the
        // primary text colour so it reads as interactive without painting
        // a button-like background that would feel heavy in a dense table.
        transition: "color 0.14s ease",
        "&:hover": { color: "text.primary" },
      }}
    >
      <Box
        component="span"
        sx={{
          overflow: "hidden",
          textOverflow: "ellipsis",
          whiteSpace: "nowrap",
          minWidth: 0,
        }}
      >
        {value}
      </Box>
      <Tooltip
        // Suppress the tooltip while the "Copied!" feedback is on screen —
        // the icon + colour change is enough confirmation, and a tooltip
        // whose title flips mid-animation was reading as the source of the
        // jitter (MUI re-measures + re-positions the popper on every text
        // change, and that work landed on the same frames as the icon
        // cross-fade below).
        title={copied ? "" : idleTitle}
        placement="top"
        arrow
        disableInteractive
      >
        <IconButton
          size="small"
          className="copy-affordance"
          onClick={handleCopy}
          aria-label={idleTitle}
          sx={{
            width: 22,
            height: 22,
            p: 0,
            flexShrink: 0,
            // The IconButton itself doesn't carry a `color` any more —
            // each stacked icon paints itself directly so the cross-fade
            // doesn't have to also animate `currentColor`. Without this
            // separation the incoming `ContentCopy` icon would briefly
            // render green (inheriting the still-mid-transition success
            // colour) and then "snap" to grey, which is the small jitter
            // that was visible at the end of the 1.2 s window.
            "&:hover": { backgroundColor: "transparent" },
          }}
        >
          {/* Two icons stacked on the same 14×14 spot, cross-faded by
              opacity + scale. Each icon owns its own colour so the
              transition is a pure visual swap with no `currentColor`
              re-flow. The slight scale gives the swap a designed
              "pop / dismiss" feel instead of a hard cut, which is what
              the eye reads as a glitch when only opacity changes. */}
          <Box
            component="span"
            sx={{
              position: "relative",
              width: 14,
              height: 14,
              display: "inline-block",
              "& > svg": {
                position: "absolute",
                top: 0,
                left: 0,
                fontSize: 14,
                transformOrigin: "center",
                transition:
                  "opacity 0.22s cubic-bezier(0.4, 0, 0.2, 1), transform 0.22s cubic-bezier(0.4, 0, 0.2, 1)",
                // Force the icon's transform onto its own compositor
                // layer so the scale animates on the GPU and never
                // shares a frame budget with the surrounding row's
                // hover / table re-renders.
                willChange: "opacity, transform",
              },
            }}
          >
            <ContentCopyIcon
              sx={{
                color: "text.secondary",
                opacity: copied ? 0 : 1,
                transform: copied ? "scale(0.7)" : "scale(1)",
              }}
            />
            <CheckIcon
              sx={{
                color: "success.main",
                opacity: copied ? 1 : 0,
                transform: copied ? "scale(1)" : "scale(0.7)",
              }}
            />
          </Box>
        </IconButton>
      </Tooltip>
    </Box>
  );
}
