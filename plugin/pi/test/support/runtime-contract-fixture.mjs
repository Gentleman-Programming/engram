import { readFileSync } from "node:fs";

// Opt in only at synthetic metadata endpoints; never intercept real Go handlers.
export const runtimeProjection = JSON.parse(readFileSync(new URL("../../runtime-contract.json", import.meta.url), "utf8"));
export const fixtureInstanceID = "00000000000000000000000000000000";
export const fixtureContract = () => ({ ...structuredClone(runtimeProjection), instance_id: fixtureInstanceID });
export function runtimeMetadataResponse(path) {
  if (path === "/runtime-contract") return Response.json(fixtureContract());
}
