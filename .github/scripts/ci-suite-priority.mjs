// Exempt only the reviewed scheduling preamble from the existing suite guards.
// Any extra dependency, event filter or step condition remains visible to them.
export function withoutSuitePriority(body) {
  return body.replace(
    '    needs: [unit-tests, e2e-tests, plugin-tests]\n    if: ${{ !cancelled() }}\n',
    '',
  );
}
