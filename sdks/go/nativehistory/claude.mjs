// This module is embedded in the Go reader and runs with the user's Node
// installation. Resolve the vendor SDK from the configured adapter installation.
import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";

try {
    const [anchor, id, dir, offset, limit] = process.argv.slice(1);
    const installed = createRequire(anchor);
    const { getSessionMessages, getSessionInfo } = await import(
        pathToFileURL(installed.resolve("@anthropic-ai/claude-agent-sdk")).href
    );
    const messages = await getSessionMessages(id, {
        dir, offset: Number(offset), limit: Number(limit), includeSystemMessages: true
    });
    if (messages.length === 0 && !(await getSessionInfo(id, { dir }))) {
        throw new Error("no retained session in this project");
    }
    process.stdout.write(JSON.stringify(messages));
} catch (error) {
    process.stderr.write(String(error.message));
    process.exitCode = 1;
}
