# Golden frames

Each `.golden` file here is one approved TUI screen frame (ANSI escapes
included), compared byte for byte by `TestGoldenFrames` and
`TestGoldenHelpPage` in `internal/tui/golden_test.go`. The suite pins the
display so unapproved visual drift fails loudly before it ships.

## Recording and approving

Goldens are approved by a human, never auto-generated:

```sh
# record every frame (creates or refreshes the .golden files)
BF_GOLDEN_UPDATE=1 go test ./internal/tui

# review the frames like any code change, then commit them
git add internal/tui/testdata/golden
```

A missing golden fails the run with recording instructions — it is never
silently accepted. A drifted frame fails with both frames printed and
re-records only when you run the `BF_GOLDEN_UPDATE=1` command above and
commit the result.

## Determinism

- The fixtures stamp the model with a fixed clock (`goldenTime`), so the
  `checked 15:04` header stamp is part of the frame.
- Spinner frames advance only through the `tick` helper (one controlled
  `spinner.TickMsg` per tick), so the spinner phase in a frame is a pure
  function of the fixture's tick count.
- Capabilities (UTF-8 / ASCII glyphs, color / `NO_COLOR`) are pinned per
  case through the test environment, and lipgloss renders styles from the
  style values alone, so a frame only ever depends on the case's inputs.
