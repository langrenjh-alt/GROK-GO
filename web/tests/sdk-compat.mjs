import assert from "node:assert/strict";
import Anthropic from "@anthropic-ai/sdk";
import OpenAI from "openai";

const serverURL = process.argv[2];
if (!serverURL) throw new Error("gateway URL is required");

const openai = new OpenAI({ apiKey: "sdk-test-key", baseURL: `${serverURL}/v1`, maxRetries: 0 });
const models = await openai.models.list();
assert.ok(models.data.some((model) => model.id === "grok-chat"));

const chat = await openai.chat.completions.create({
  model: "grok-chat",
  messages: [{ role: "user", content: "compatibility" }],
});
assert.equal(chat.choices[0]?.message.content, "sdk-compatible");

const stream = await openai.chat.completions.create({
  model: "grok-chat",
  messages: [{ role: "user", content: "stream compatibility" }],
  stream: true,
});
let streamed = "";
for await (const chunk of stream) streamed += chunk.choices[0]?.delta.content ?? "";
assert.equal(streamed, "sdk-compatible");

const response = await openai.responses.create({ model: "grok-chat", input: "compatibility" });
assert.equal(response.status, "completed");
assert.ok(response.output.some((item) => item.type === "message"));

const image = await openai.images.generate({ model: "grok-image", prompt: "compatibility" });
assert.equal(image.data?.[0]?.url, "https://cdn.test/image.png");

const anthropic = new Anthropic({ apiKey: "sdk-test-key", baseURL: serverURL, maxRetries: 0 });
const message = await anthropic.messages.create({
  model: "grok-chat",
  max_tokens: 64,
  messages: [{ role: "user", content: "compatibility" }],
});
assert.equal(message.content[0]?.type, "text");
assert.equal(message.content[0]?.text, "sdk-compatible");

process.stdout.write(JSON.stringify({ openai: true, anthropic: true }));
