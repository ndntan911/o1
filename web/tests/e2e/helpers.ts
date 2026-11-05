import type { APIRequestContext } from '@playwright/test';

const MAILPIT_API =
	process.env.E2E_MAILPIT_API ?? 'http://localhost:8025/api/v1';
const POLL_INTERVAL_MS = 500;
const TIMEOUT_MS = 30_000;
const VERIFY_LINK_PATTERN =
	/https?:\/\/[^\s"'<>]*\/verify-email\?token=[^\s"'<>]+/;

interface MailpitAddress {
	Address: string;
	Name?: string;
}

interface MailpitMessageSummary {
	ID: string;
	To?: MailpitAddress[];
}

interface MailpitMessagesResponse {
	messages?: MailpitMessageSummary[];
}

interface MailpitMessageDetail {
	Text?: string;
	HTML?: string;
}

function sleep(milliseconds: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function decodeHtmlEntities(value: string): string {
	return value
		.replace(/&amp;/g, '&')
		.replace(/&#38;/g, '&')
		.replace(/&quot;/g, '"')
		.replace(/&#39;/g, "'");
}

function extractVerificationLink(body: string): string | undefined {
	const decoded = decodeHtmlEntities(body);
	const match = decoded.match(VERIFY_LINK_PATTERN);
	if (!match) return undefined;
	return match[0].replace(/[),.;]+$/, '');
}

function messageRecipients(message: MailpitMessageSummary): string[] {
	return (message.To ?? []).map((address) =>
		address.Address.trim().toLowerCase(),
	);
}

/**
 * Polls Mailpit for the verification email sent to `email` and returns the
 * first `/verify-email?token=...` link found in the message text or HTML.
 */
export async function waitForVerificationLink(
	request: APIRequestContext,
	email: string,
): Promise<string> {
	const wanted = email.trim().toLowerCase();
	const deadline = Date.now() + TIMEOUT_MS;
	let lastError = 'no message addressed to this recipient yet';

	while (Date.now() < deadline) {
		try {
			const listResponse = await request.get(
				`${MAILPIT_API}/messages?limit=50`,
			);
			if (listResponse.ok()) {
				const list = (await listResponse.json()) as MailpitMessagesResponse;
				const message = (list.messages ?? []).find((candidate) =>
					messageRecipients(candidate).includes(wanted),
				);

				if (message) {
					const detailResponse = await request.get(
						`${MAILPIT_API}/message/${encodeURIComponent(message.ID)}`,
					);
					if (detailResponse.ok()) {
						const detail =
							(await detailResponse.json()) as MailpitMessageDetail;
						const link =
							extractVerificationLink(detail.Text ?? '') ??
							extractVerificationLink(detail.HTML ?? '');
						if (link) return link;
						lastError = 'the message did not contain a verification link';
					} else {
						lastError = `Mailpit message ${message.ID} returned ${detailResponse.status()}`;
					}
				}
			} else {
				lastError = `Mailpit messages endpoint returned ${listResponse.status()}`;
			}
		} catch (error) {
			lastError = error instanceof Error ? error.message : String(error);
		}

		await sleep(POLL_INTERVAL_MS);
	}

	throw new Error(
		`Timed out after ${TIMEOUT_MS / 1000}s waiting for a verification email for ${email}: ${lastError}`,
	);
}
