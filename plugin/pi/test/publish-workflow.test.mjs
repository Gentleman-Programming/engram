import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const workflow = readFileSync(new URL("../../../.github/workflows/publish-pi.yml", import.meta.url), "utf8").replaceAll("\r\n", "\n");
const testStep = "      - name: Run Pi plugin tests\n        working-directory: plugin/pi\n        run: npm test\n";
const publishStep = "      - name: Publish to npm with provenance";

test("Pi publication tests run fail-closed immediately before publishing", () => {
	const testStepIndex = workflow.indexOf(testStep);
	const publishStepIndex = workflow.indexOf(publishStep);

	assert.ok(testStepIndex >= 0, "the publish workflow must run npm test from plugin/pi");
	assert.ok(publishStepIndex >= 0, "the publish workflow must publish the Pi package");
	assert.ok(testStepIndex < publishStepIndex, "npm test must run before npm publish");
	assert.match(
		workflow.slice(testStepIndex + testStep.length, publishStepIndex),
		/^\s*$/,
		"the test step must immediately precede the publish step",
	);
	assert.doesNotMatch(
		workflow.slice(testStepIndex, publishStepIndex),
		/continue-on-error\s*:/,
		"the test step must not opt out of fail-fast behavior",
	);
});
