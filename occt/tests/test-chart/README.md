# `test-chart`

Library charts like `occt` declare no templates of their own and cannot be
deployed, so something has to call the transpiler and render the result. This
chart is that caller — a stand-in for a real consuming chart, used only to test
`occt`.

It is not published and has no purpose outside the test suite. The tests that
render it, and the golden files they compare against, live one level up in
[`../`](../README.md).
