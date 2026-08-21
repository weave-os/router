// Pi 0.74 intentionally ignores files named *.test.ts when loading extensions.
// Use a non-test wrapper so the E2E harness exercises these modules through
// Pi's own TypeScript loader on both the legacy and current runtimes.
import "./savings.test.js";
import "./force-model.test.js";
import "./ui.test.js";
import "./compaction.test.js";
import "./routed-model.test.js";

// Pi 0.74 also requires an extension-shaped default export before evaluating
// the module. Test registration itself happens through the side-effect imports.
export default function unitSuite(): void {}
