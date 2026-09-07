import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const workflow = readFileSync(new URL("../../../.github/workflows/publish-pi.yml", import.meta.url), "utf8").replaceAll("\r\n", "\n");

function namedStep(name) {
	const match = workflow.match(new RegExp(
		`^      - name: ${name}\\n[\\s\\S]*?(?=^      - |(?![\\s\\S]))`,
		"m",
	));

	assert.ok(match, `the publish workflow must contain the ${name} step`);
	return { index: match.index, end: match.index + match[0].length, text: match[0] };
}

test("Pi publication tests run fail-closed immediately before publishing", () => {
	const testStep = namedStep("Run Pi plugin tests");
	const publishStep = namedStep("Publish to npm with provenance");

	assert.match(testStep.text, /^        working-directory: plugin\/pi$/m, "the test step must run from plugin/pi");
	assert.match(testStep.text, /^        run: npm test$/m, "the test step must run npm test");
	assert.match(publishStep.text, /^        working-directory: plugin\/pi$/m, "the publish step must run from plugin/pi");
	assert.match(publishStep.text, /^        run: npm publish --provenance --access public$/m, "the publish step must run the required npm publish command");
	assert.equal(testStep.end, publishStep.index, "the test step must immediately precede the publish step");
	assert.doesNotMatch(
		testStep.text,
		/^        continue-on-error\s*:/m,
		"the test step must not opt out of fail-fast behavior",
	);
	assert.doesNotMatch(testStep.text, /^        if\s*:/m, "the test step must not be conditional");
	assert.doesNotMatch(publishStep.text, /^        if\s*:/m, "the publish step must not be conditional");
	assert.doesNotMatch(publishStep.text, /^        continue-on-error\s*:/m, "the publish step must not opt out of fail-fast behavior");
});
