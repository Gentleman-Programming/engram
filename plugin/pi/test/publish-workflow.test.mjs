import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const workflow = readFileSync(new URL("../../../.github/workflows/publish-pi.yml", import.meta.url), "utf8").replaceAll("\r\n", "\n");

function namedStep(name, source = workflow) {
	const match = source.match(new RegExp(
		`^      - name: ${name}\\n[\\s\\S]*?(?=^      - |(?![\\s\\S]))`,
		"m",
	));

	assert.ok(match, `the publish workflow must contain the ${name} step`);
	return { index: match.index, end: match.index + match[0].length, text: match[0] };
}

function assertGuardedPublication(source) {
	const guard = namedStep("Verify Pi release version", source);
	const tests = namedStep("Run Pi plugin tests", source);
	const publish = namedStep("Publish to npm with provenance", source);

	assert.doesNotMatch(guard.text, /^        (?:if|continue-on-error)\s*:/m, "the ref guard must run on push and workflow_dispatch and fail closed");
	assert.match(guard.text, /^          RELEASE_REF: \$\{\{ github\.ref \}\}$/m, "the guard must receive the actual full event ref");
	assert.match(guard.text, /^        run: node test\/release-contract\.mjs "\$RELEASE_REF"$/m);
	assert.ok(guard.end <= tests.index && tests.end <= publish.index, "the ref guard and tests must precede publication");
	const publishJob = source.match(/^  publish:\n[\s\S]*?(?=^  [\w-]+:|$(?![\s\S]))/m);
	assert.ok(publishJob, "the guarded publish job must exist");
	assert.ok(guard.index >= publishJob.index && publish.index < publishJob.index + publishJob[0].length,
		"the ref guard, tests, and publication must stay in the publish job");
	assert.equal((source.match(/^        run: npm publish\b/gm) ?? []).length, 1, "there must be no alternate npm publish step");
}

test("both event routes verify the full ref before any npm publication", () => {
	assertGuardedPublication(workflow);
});

test("a later independent publishing job cannot bypass the ref guard", () => {
	const publishStep = `      - name: Publish to npm with provenance
        working-directory: plugin/pi
        run: npm publish --provenance --access public`;
	const unguardedJob = `  unguarded-publish:
    runs-on: ubuntu-latest
    steps:
${publishStep}
`;
	assert.throws(() => assertGuardedPublication(`${workflow.replace(publishStep, "").trimEnd()}\n${unguardedJob}`),
		/the ref guard, tests, and publication must stay in the publish job/);
});

test("an unrelated later job does not invalidate guarded publication", () => {
	const unrelatedJob = `  housekeeping:
    runs-on: ubuntu-latest
    steps:
      - run: echo done
`;
	assertGuardedPublication(`${workflow.trimEnd()}\n${unrelatedJob}`);
});

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
