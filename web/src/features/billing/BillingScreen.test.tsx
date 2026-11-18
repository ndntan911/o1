import { beforeEach, expect, test } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import type { Workspace } from '@/lib/api/types';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import { BillingScreen } from './BillingScreen';

const ORIGIN = 'http://localhost:5173';
const workspace: Workspace = {
	id: 'w1',
	name: 'Acme',
	slug: 'acme',
	role: 'admin',
};

const free = {
	plan: {
		key: 'free',
		name: 'Free',
		monitors: 5,
		channels: 3,
		api_keys: 2,
		price_cents: 0,
	},
	status: 'active',
	provider: 'internal',
	current_period_end: null,
	trial_end: null,
	cancel_at_period_end: false,
	mode: 'sandbox',
};

const pro = {
	...free,
	plan: { ...free.plan, key: 'pro', name: 'Pro', monitors: 50 },
	status: 'trialing',
	trial_end: '2026-12-02T00:00:00Z',
};

beforeEach(() => {
	let billingCalls = 0;
	server.use(
		http.get(`${ORIGIN}/api/v1/plans`, () =>
			HttpResponse.json({
				plans: [free.plan, pro.plan],
			}),
		),
		http.get(`${ORIGIN}/api/v1/workspaces/w1/billing`, () => {
			billingCalls += 1;
			return HttpResponse.json(billingCalls > 1 ? pro : free);
		}),
		http.post(`${ORIGIN}/api/v1/workspaces/w1/billing/checkout`, () =>
			HttpResponse.json({ url: '/billing/sandbox?token=test-token' }),
		),
		http.post(
			`${ORIGIN}/api/v1/workspaces/w1/billing/sandbox/complete`,
			() => new HttpResponse(null, { status: 204 }),
		),
	);
});

test('upgrades through the sandbox checkout and refreshes the plan', async () => {
	const user = userEvent.setup();
	renderWithProviders(<BillingScreen workspace={workspace} />);

	expect(await screen.findByText('Free')).toBeInTheDocument();
	await user.click(
		await screen.findByRole('button', { name: /upgrade to pro/i }),
	);

	expect(await screen.findByText('Sandbox checkout')).toBeInTheDocument();
	await user.click(
		screen.getByRole('button', { name: /complete sandbox payment/i }),
	);

	await waitFor(() => expect(screen.getByText('Pro')).toBeInTheDocument());
});

test('a paid plan offers cancellation instead of upgrades', async () => {
	server.use(
		http.get(`${ORIGIN}/api/v1/workspaces/w1/billing`, () =>
			HttpResponse.json(pro),
		),
	);
	renderWithProviders(<BillingScreen workspace={workspace} />);

	expect(
		await screen.findByRole('button', { name: /cancel at period end/i }),
	).toBeInTheDocument();
	expect(
		screen.queryByRole('button', { name: /upgrade to pro/i }),
	).not.toBeInTheDocument();
});
