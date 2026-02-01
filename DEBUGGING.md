# Debugging h2

## Dumping midterm's screen state

To inspect what midterm stores for each row (format regions, colors, content),
add a Ctrl+G handler in `readInput()`:

```go
case 0x07: // Ctrl+G: dump debug state
    w.dumpDebug()
```

And add this method:

```go
func (w *wrapper) dumpDebug() {
    f, _ := os.Create("/tmp/h2-debug.log")
    if f == nil { return }
    defer f.Close()

    childRows := w.rows - 2
    fmt.Fprintf(f, "Screen: %d rows x %d cols\n\n", w.rows, w.cols)

    for row := 0; row < childRows && row < len(w.vt.Content); row++ {
        line := w.vt.Content[row]
        fmt.Fprintf(f, "=== Row %d (content len: %d) ===\n", row, len(line))
        regionIdx := 0
        for region := range w.vt.Format.Regions(row) {
            fgStr, bgStr := "none", "none"
            if region.F.Fg != nil { fgStr = fmt.Sprintf("fg=%s", region.F.Fg.Sequence(false)) }
            if region.F.Bg != nil { bgStr = fmt.Sprintf("bg=%s", region.F.Bg.Sequence(true)) }
            fmt.Fprintf(f, "  region[%d]: size=%d %s %s props=%d render=%q\n",
                regionIdx, region.Size, fgStr, bgStr, region.F.Properties, region.F.Render())
            regionIdx++
        }
        trimmed := strings.TrimRight(string(line), " ")
        if len(trimmed) == 0 {
            fmt.Fprintf(f, "  content: (all spaces, len %d)\n", len(line))
        } else {
            fmt.Fprintf(f, "  content: %q\n", trimmed)
        }
        fmt.Fprintln(f)
    }
}
```

Press Ctrl+G while running to write `/tmp/h2-debug.log`.

## Capturing raw PTY output

To see the exact escape sequences the child sends, add raw logging in
`pipeOutput()`:

```go
func (w *wrapper) pipeOutput() {
    rawLog, _ := os.Create("/tmp/h2-raw.log")
    // ...
    if n > 0 {
        if rawLog != nil { rawLog.Write(buf[:n]) }
        // ... rest of pipeOutput
    }
}
```

Then inspect with `cat -v /tmp/h2-raw.log` or `xxd /tmp/h2-raw.log`.

Useful things to search for in raw output:
- `48;5;N` or `48;2;R;G;B` — background color SGR sequences
- `\033]10;?` / `\033]11;?` — OSC 10/11 color queries from the child
- `\033[c` — DA1 capability query
