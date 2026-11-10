import { execFileSync } from 'node:child_process';
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

/**
 * Thrown only when Mailpit is reachable but no verification email (or link)
 * appeared within the timeout. Transport and malformed-response errors are
 * thrown as ordinary errors so the test fails instead of hiding them behind
 * the SQL verification fallback.
 */
export class MailpitTimeoutError extends Error {
	constructor(message: string) {
		super(message);
		this.name = 'MailpitTimeoutError';
	}
}

function sleep(milliseconds: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

interface ComposePsqlOptions {
	variables?: Record<string, string>;
	tuplesOnly?: boolean;
}

/**
 * Runs SQL against the Compose PostgreSQL service. Values passed through
 * `variables` are bound with psql's `:'name'` quoting rather than interpolated
 * into the SQL string.
 */
export function composePsql(
	sql: string,
	{ variables = {}, tuplesOnly = false }: ComposePsqlOptions = {},
): string {
	const args = [
		'compose',
		'-f',
		'../compose.yaml',
		'exec',
		'-T',
		'postgres',
		'psql',
		'-U',
		'o1',
		'-d',
		'o1',
		'-v',
		'ON_ERROR_STOP=1',
	];
	for (const [name, value] of Object.entries(variables)) {
		args.push('-v', `${name}=${value}`);
	}
	if (tuplesOnly) args.push('-tA');

	return execFileSync('docker', args, {
		stdio: 'pipe',
		encoding: 'utf8',
		input: sql,
	});
}

/**
 * Runs a command inside a Compose service and returns its stdout. E2E checks
 * use this to reach internal listeners that are not published to the host,
 * such as the metrics endpoints on the Compose network.
 */
export function composeExec(service: string, command: string[]): string {
	return execFileSync(
		'docker',
		['compose', '-f', '../compose.yaml', 'exec', '-T', service, ...command],
		{ stdio: 'pipe', encoding: 'utf8' },
	);
}

/**
 * Inserts a non-terminal River job scheduled in the future so the worker's
 * queue gauges have a sample without racing a real probe job. The job never
 * becomes runnable, so the worker does not attempt to execute it.
 */
export function seedQueuedJob(queue = 'probe'): void {
	composePsql(
		`INSERT INTO river_job (kind, args, queue, state, scheduled_at)
		 VALUES ('e2e_metrics', '{}'::jsonb, :'queue', 'available', now() + interval '1 day');`,
		{ variables: { queue } },
	);
}

/**
 * Looks up the internal workspace id from its public slug. Bearer API routes
 * address workspaces by id, while the dashboard URLs only carry the slug.
 */
export function workspaceIdForSlug(slug: string): string {
	const output = composePsql(
		`SELECT id FROM workspaces WHERE slug = :'slug' LIMIT 1;`,
		{ variables: { slug }, tuplesOnly: true },
	).trim();
	if (!output) {
		throw new Error(`No workspace found with slug ${slug}`);
	}
	return output;
}

export interface SecurityEventRow {
	kind: string;
	detail: Record<string, unknown>;
	actor_user_id: string | null;
	actor_api_key_id: string | null;
	created_at: string;
}

/**
 * Reads security events for a workspace directly from PostgreSQL. Passing a
 * kind filters the result; an empty kind returns every event for the
 * workspace, newest first. Details are decoded from JSONB into objects.
 */
export function securityEvents(
	workspaceID: string,
	kind = '',
): SecurityEventRow[] {
	const output = composePsql(
		`SELECT COALESCE(
			jsonb_agg(
				jsonb_build_object(
					'kind', kind,
					'detail', detail,
					'actor_user_id', actor_user_id,
					'actor_api_key_id', actor_api_key_id,
					'created_at', created_at
				)
				ORDER BY created_at DESC, id DESC
			),
			'[]'::jsonb
		)::text
		FROM security_events
		WHERE workspace_id = :'workspace_id'
		  AND (:'kind' = '' OR kind = :'kind');`,
		{ variables: { workspace_id: workspaceID, kind }, tuplesOnly: true },
	);
	return JSON.parse(output.trim()) as SecurityEventRow[];
}

export interface SeededIncident {
	incidentId: string;
	workspaceSlug: string;
}

/**
 * Inserts an open incident and its opening timeline event directly through
 * psql so the incident UI can be exercised without waiting for a probe to
 * fail. Returns the incident id and the slug of the workspace that owns the
 * named monitor.
 */
export function seedIncident(monitorName: string): SeededIncident {
	const output = composePsql(
		`WITH monitor AS (
			SELECT m.id, m.workspace_id, w.slug
			FROM monitors m
			JOIN workspaces w ON w.id = m.workspace_id
			WHERE m.name = :'monitor_name'
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT 1
		), incident AS (
			INSERT INTO incidents (
				id, workspace_id, monitor_id, status, started_at, last_notified_at
			)
			SELECT gen_random_uuid(), monitor.workspace_id, monitor.id, 'open', now(), now()
			FROM monitor
			RETURNING id
		), event AS (
			INSERT INTO incident_events (id, incident_id, kind, detail)
			SELECT gen_random_uuid(), incident.id, 'opened', '{}'::jsonb
			FROM incident
		)
		SELECT incident.id, monitor.slug
		FROM incident
		CROSS JOIN monitor;`,
		{ variables: { monitor_name: monitorName }, tuplesOnly: true },
	);

	const [incidentId, workspaceSlug] = output.trim().split('|');
	if (!incidentId || !workspaceSlug) {
		throw new Error(`Could not seed an incident for monitor ${monitorName}`);
	}
	return { incidentId, workspaceSlug };
}

/**
 * Inserts one down probe for yesterday and one up probe for today for the
 * named monitor so the public status page has a non-empty daily history.
 */
export function seedProbeResults(monitorName: string): void {
	composePsql(
		`
		WITH monitor AS (
			SELECT m.id, m.workspace_id
			FROM monitors m
			WHERE m.name = :'monitor_name'
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT 1
		)
		INSERT INTO probe_results (
			id, monitor_id, workspace_id, scheduled_for, checked_at, status, latency_ms
		)
		SELECT gen_random_uuid(), monitor.id, monitor.workspace_id,
		       now() - interval '1 day', now() - interval '1 day', 'down', 100
		FROM monitor
		UNION ALL
		SELECT gen_random_uuid(), monitor.id, monitor.workspace_id,
		       now(), now(), 'up', 100
		FROM monitor;
	`,
		{ variables: { monitor_name: monitorName } },
	);
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
		const listResponse = await request.get(`${MAILPIT_API}/messages?limit=50`);
		if (!listResponse.ok()) {
			throw new Error(
				`Mailpit messages endpoint returned ${listResponse.status()}`,
			);
		}

		const list = (await listResponse.json()) as MailpitMessagesResponse;
		const message = (list.messages ?? []).find((candidate) =>
			messageRecipients(candidate).includes(wanted),
		);

		if (message) {
			const detailResponse = await request.get(
				`${MAILPIT_API}/message/${encodeURIComponent(message.ID)}`,
			);
			if (!detailResponse.ok()) {
				throw new Error(
					`Mailpit message ${message.ID} returned ${detailResponse.status()}`,
				);
			}

			const detail = (await detailResponse.json()) as MailpitMessageDetail;
			const link =
				extractVerificationLink(detail.Text ?? '') ??
				extractVerificationLink(detail.HTML ?? '');
			if (link) return link;
			lastError = 'the message did not contain a verification link';
		}

		await sleep(POLL_INTERVAL_MS);
	}

	throw new MailpitTimeoutError(
		`Timed out after ${TIMEOUT_MS / 1000}s waiting for a verification email for ${email}: ${lastError}`,
	);
}
