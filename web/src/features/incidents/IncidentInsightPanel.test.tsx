import { describe, expect, test } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { IncidentEvent, IncidentInsight } from '@/lib/api/types';
import { IncidentInsightPanel } from './IncidentInsightPanel';

const workspaceId = '11111111-1111-1111-1111-111111111111';
const incidentId = '22222222-2222-2222-2222-222222222222';
const eventId = '33333333-3333-3333-3333-333333333333';

const events: IncidentEvent[] = [
	{
		id: eventId,
		kind: 'opened',
		actor_user_id: null,
		detail: { next: 'down' },
		created_at: '2026-11-20T09:00:00Z',
	},
];

function insight(overrides: Partial<IncidentInsight> = {}): IncidentInsight {
	return {
		id: '44444444-4444-4444-4444-444444444444',
		workspace_id: workspaceId,
		incident_id: incidentId,
		model: 'stub',
		prompt_version: '2026-11-21.1',
		payload_hash: 'abc',
		status: 'ok',
		result: {
			sufficient: true,
			assessment: 'The target stopped answering within the configured timeout.',
			hypotheses: [
				{
					rank: 1,
					cause: 'Timeouts above the 10s limit',
					confidence: 'high',
					evidence: [
						{ kind: 'event', event_id: eventId, note: 'consecutive failures' },
					],
					falsified_by: 'A successful check in the same window',
				},
			],
		},
		input_tokens: 10,
		output_tokens: 20,
		created_by: null,
		created_at: '2026-11-20T09:30:00Z',
		...overrides,
	};
}

const base = `/api/v1/workspaces/${workspaceId}/incidents/${incidentId}/insights`;

describe('IncidentInsightPanel', () => {
	test('offers to explain the incident when nothing is stored', async () => {
		server.use(
			http.get(`${base}/latest`, () => new HttpResponse(null, { status: 404 })),
		);

		renderWithProviders(
			<IncidentInsightPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				events={events}
			/>,
		);

		expect(
			await screen.findByRole('button', { name: /explain this incident/i }),
		).toBeInTheDocument();
	});

	test('shows a generated assessment with its evidence', async () => {
		server.use(
			http.get(`${base}/latest`, () => new HttpResponse(null, { status: 404 })),
			http.post(base, () => HttpResponse.json(insight())),
		);

		renderWithProviders(
			<IncidentInsightPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				events={events}
			/>,
		);

		await userEvent.click(
			await screen.findByRole('button', { name: /explain this incident/i }),
		);

		await waitFor(() =>
			expect(screen.getByText(/stopped answering/i)).toBeInTheDocument(),
		);
		expect(
			screen.getByText('Timeouts above the 10s limit'),
		).toBeInTheDocument();
		expect(screen.getByText(/high confidence/i)).toBeInTheDocument();
		expect(screen.getByText(/timeline: opened/i)).toBeInTheDocument();
		expect(screen.getByText(/would be wrong if/i)).toBeInTheDocument();
	});

	test('labels a claim it could not verify', async () => {
		server.use(
			http.get(`${base}/latest`, () =>
				HttpResponse.json(
					insight({
						result: {
							sufficient: true,
							assessment: 'Something failed.',
							hypotheses: [
								{
									rank: 1,
									cause: 'Certificate expiry',
									confidence: 'medium',
									evidence: [],
									unsupported: true,
								},
							],
						},
					}),
				),
			),
		);

		renderWithProviders(
			<IncidentInsightPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				events={events}
			/>,
		);

		expect(await screen.findByText(/unverified/i)).toBeInTheDocument();
	});

	test('says so when the evidence is too thin', async () => {
		server.use(
			http.get(`${base}/latest`, () =>
				HttpResponse.json(
					insight({
						status: 'insufficient',
						result: {
							sufficient: false,
							assessment: 'Only one check is recorded.',
							hypotheses: [],
						},
					}),
				),
			),
		);

		renderWithProviders(
			<IncidentInsightPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				events={events}
			/>,
		);

		expect(await screen.findByText(/not enough evidence/i)).toBeInTheDocument();
	});
});

test('survives a stored row whose hypotheses are null', async () => {
	server.use(
		http.get(`${base}/latest`, () =>
			HttpResponse.json({
				...insight({
					status: 'failed',
					result: {
						sufficient: false,
						assessment: 'The model call failed. Nothing was analysed.',
						// A row written before the API guaranteed an array.
						hypotheses: null,
					} as unknown as IncidentInsight['result'],
				}),
			}),
		),
	);

	renderWithProviders(
		<IncidentInsightPanel
			workspaceId={workspaceId}
			incidentId={incidentId}
			events={events}
		/>,
	);

	expect(
		await screen.findByText('The model call failed. Nothing was analysed.'),
	).toBeInTheDocument();
});
