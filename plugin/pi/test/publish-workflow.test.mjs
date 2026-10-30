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

const ciWorkflow = readFileSync(new URL("../../../.github/workflows/ci.yml", import.meta.url), "utf8").replaceAll("\r\n", "\n");

for (const [label, source, jobName] of [
	["CI", ciWorkflow, "plugin-tests"],
	["publish preflight", workflow, "publish"],
]) {
	test(`${label} prepares locked Go prerequisites before the offline Pi suite`, () => {
		const job = source.match(new RegExp(`^  ${jobName}:\\n[\\s\\S]*?(?=^  [\\w-]+:|$(?![\\s\\S]))`, "m"));
		assert.ok(job, `${jobName} must exist`);
		const setup = namedStep("Set up Go for Pi checks", job[0]);
		const build = namedStep("Build release candidate checker", job[0]);
		const tests = namedStep("Run Pi plugin tests", job[0]);
		assert.match(setup.text, /^        uses: actions\/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6$/m);
		assert.match(setup.text, /^          go-version-file: go\.mod$/m);
		assert.match(build.text, /^        run: go build -mod=readonly -o "\$RUNNER_TEMP\/engram-release-candidate" \.\/tools\/release-candidate$/m);
		assert.doesNotMatch(build.text, /^        working-directory:/m, "build must use the module root");
		assert.ok(setup.end <= build.index && build.end <= tests.index, "Go setup and cache warmup must precede the offline suite");
		for (const step of [setup, build]) {
			assert.doesNotMatch(step.text, /^        (?:if|continue-on-error)\s*:/m, "prerequisites must fail closed on every test route");
		}
	});
}

test("both event routes verify the full ref before any npm publication", () => {
	assertGuardedPublication(workflow);
});

test("a later independent publishing job cannot bypass the ref guard", () => {
	const publishStep = `      - name: Publish to npm with provenance
        working-directory: plugin/pi
        run: npm publish --provenance --access public --tag candidate`;
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
	assert.match(publishStep.text, /^        run: npm publish --provenance --access public --tag candidate$/m, "the publish step must run the required npm publish command");
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
