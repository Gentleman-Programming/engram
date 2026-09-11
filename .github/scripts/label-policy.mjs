import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const defaultPolicyPath = path.join(root, '.github', 'labels.yml');

function list(value = '') {
  return value.slice(1, -1).split(',').map((item) => item.trim().replace(/^['"]|['"]$/g, '')).filter(Boolean);
}

function metadata(text) {
  const fields = Object.fromEntries([...text.matchAll(/(\w+):\s*(\[[^\]]*\]|[^,}]+)/g)]
    .map(([, key, value]) => [key, value.trim()]));
  return {
    ...fields,
    applies_to: list(fields.applies_to),
    required_on: list(fields.required_on),
    deprecated_aliases: list(fields.deprecated_aliases),
    protected_exceptions: list(fields.protected_exceptions),
    protected_exception: fields.protected_exception === 'true',
  };
}

export function loadLabelPolicy(policyPath = defaultPolicyPath) {
  const source = fs.readFileSync(policyPath, 'utf8');
  const entries = [...source.matchAll(/^- name:\s*"([^"]+)"([\s\S]*?)(?=^- name:|$(?![\s\S]))/gm)].map((match) => {
    const policyMatch = match[2].match(/^\s+policy:\s*\{(.*)\}\s*$/m);
    return { name: match[1], policy: metadata(policyMatch?.[1] ?? '') };
  });
  const namespaces = new Map();
  const aliases = new Map();
  const migrations = new Map();
  const protectedExceptions = new Set();

  for (const entry of entries) {
    const { policy } = entry;
    if (policy.namespace) {
      if (!policy.owner || !policy.cardinality || policy.applies_to.length === 0) {
        throw new Error(`namespace ${policy.namespace} requires owner, cardinality, and applies_to`);
      }
      if (policy.required_on.some((target) => !policy.applies_to.includes(target))) {
        throw new Error(`namespace ${policy.namespace} required_on must be a subset of applies_to`);
      }
      namespaces.set(policy.namespace, {
        owner: policy.owner,
        cardinality: policy.cardinality,
        requiredOn: policy.required_on,
        appliesTo: policy.applies_to,
      });
    }
    for (const alias of policy.deprecated_aliases) {
      if (policy.migration_precedence !== 'canonical' || policy.migration_conflict !== 'manual-review') {
        throw new Error(`deprecated alias ${alias} requires canonical precedence and manual-review conflicts`);
      }
      if (aliases.has(alias)) throw new Error(`deprecated alias ${alias} is declared more than once`);
      aliases.set(alias, entry.name);
      migrations.set(alias, {
        canonical: entry.name,
        precedence: policy.migration_precedence,
        conflict: policy.migration_conflict,
      });
    }
    for (const exception of policy.protected_exceptions) protectedExceptions.add(exception);
  }

  for (const entry of entries) {
    if (entry.name.includes(':')) {
      const namespace = entry.name.split(':', 1)[0];
      if (!namespaces.has(namespace)) throw new Error(`label ${entry.name} has no namespace policy`);
    } else if (!protectedExceptions.has(entry.name) || !entry.policy.protected_exception) {
      throw new Error(`unnamespaced label ${entry.name} is not a protected exception`);
    } else if (!entry.policy.owner) {
      throw new Error(`protected exception ${entry.name} requires owner`);
    }
  }
  for (const exception of protectedExceptions) {
    if (!entries.some((entry) => entry.name === exception && entry.policy.protected_exception)) {
      throw new Error(`protected exception ${exception} is not declared`);
    }
  }

  return { entries, namespaces, aliases, migrations, protectedExceptions };
}

function singletonErrors(policy, labels, target = 'issue') {
  const errors = [];
  for (const [namespace, rule] of policy.namespaces) {
    if (!rule.appliesTo.includes(target)) continue;
    if (rule.cardinality === 'multi-valued') continue;
    const selected = labels.filter((label) => label.startsWith(`${namespace}:`));
    const required = rule.cardinality === 'required-singleton' || rule.requiredOn.includes(target);
    if (required && selected.length === 0) {
      errors.push(`${namespace}:* requires exactly one ${namespace}:* label`);
    } else if (selected.length > 1) {
      errors.push(`${namespace}:* permits at most one label: ${selected.join(', ')}`);
    }
  }
  return errors;
}

export function validateLabels(policy, labels, target = 'issue') {
  const declared = new Map(policy.entries.map((entry) => [entry.name, entry]));
  const errors = [];
  for (const label of labels) {
    if (policy.aliases.has(label)) {
      errors.push(`deprecated label: ${label}; use ${policy.aliases.get(label)}`);
      continue;
    }
    const entry = declared.get(label);
    if (!entry) {
      errors.push(`undeclared label: ${label}`);
      continue;
    }
    if (!label.includes(':')) {
      if (!policy.protectedExceptions.has(label)) errors.push(`unnamespaced label is not protected: ${label}`);
          else if (!entry.policy.applies_to.includes(target)) errors.push(`label ${label} does not apply to ${target}`);
      continue;
    }
    const rule = policy.namespaces.get(label.split(':', 1)[0]);
    if (!rule.appliesTo.includes(target)) errors.push(`label ${label} does not apply to ${target}`);
  }
  return { errors: [...errors, ...singletonErrors(policy, labels.filter((label) => declared.has(label)), target) ] };
}

export function migrateLabels(policy, labels) {
  const mapped = labels.map((label) => policy.migrations.get(label)?.canonical ?? label);
  const migrated = [...new Set(mapped)];
  const conflicts = singletonErrors(policy, migrated).filter((error) => error.includes('permits at most one'));
  if (conflicts.length > 0) {
    return { labels, changed: false, conflicts };
  }
  return { labels: migrated, changed: migrated.join('\0') !== labels.join('\0'), conflicts: [] };
}

function run() {
  const args = process.argv.slice(2);
  const migrate = args.includes('--migrate');
  const targetIndex = args.indexOf('--target');
  const labelsIndex = args.indexOf('--labels-json');
  const target = targetIndex >= 0 ? args[targetIndex + 1] : 'pull-request';
  const encoded = labelsIndex >= 0 ? args[labelsIndex + 1] : undefined;
  if (!encoded) {
    throw new Error('usage: label-policy.mjs [--migrate | --target <issue|pull-request>] --labels-json <json-array>');
  }
  const policy = loadLabelPolicy();
  const labels = JSON.parse(encoded);
  if (migrate) {
    const result = migrateLabels(policy, labels);
    console.log(JSON.stringify(result));
    if (result.conflicts.length > 0) process.exitCode = 1;
    return;
  }
  const result = validateLabels(policy, labels, target);
  if (result.errors.length > 0) {
    console.error(`Label policy validation failed:\n${result.errors.map((error) => `- ${error}`).join('\n')}`);
    process.exitCode = 1;
  } else {
    console.log('Label policy validation passed.');
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) run();
