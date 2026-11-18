import { beforeEach, expect, test, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import type { Workspace } from '@/lib/api/types';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import { SandboxCheckoutScreen } from './SandboxCheckoutScreen';

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));

vi.mock('@tanstack/react-router', async (importOriginal) => {
	const actual =
		await importOriginal<typeof import('@tanstack/react-router')>();
	return { ...actual, useNavigate: () => navigate };
});

const ORIGIN = 'http://localhost:5173';
const workspace: Workspace = {
	id: 'w1',
	name: 'Acme',
	slug: 'acme',
	role: 'admin',
};
const token =
	btoa(JSON.stringify({ workspace_id: 'w1', plan_key: 'pro' }))
		.replace(/\+/g, '-')
		.replace(/\//g, '_')
		.replace(/=+$/, '') + '.signature';

beforeEach(() => {
	navigate.mockReset();
	server.use(
		http.post(
			`${ORIGIN}/api/v1/workspaces/w1/billing/sandbox/complete`,
			() => new HttpResponse(null, { status: 204 }),
		),
	);
});

test('completes the sandbox checkout and returns to billing', async () => {
	const user = userEvent.setup();
	renderWithProviders(
		<SandboxCheckoutScreen workspace={workspace} token={token} />,
	);

	expect(screen.getByText('Sandbox checkout')).toBeInTheDocument();
	expect(screen.getByText('pro')).toBeInTheDocument();

	await user.click(
		screen.getByRole('button', { name: /complete sandbox payment/i }),
	);
	await expect.poll(() => navigate.mock.calls.length).toBe(1);
});
