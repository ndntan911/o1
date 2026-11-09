import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { APIKey, Workspace } from '@/lib/api/types';
import { APIKeysScreen } from './APIKeysScreen';

const ORIGIN = 'http://localhost:5173';
const keysPath = `${ORIGIN}/api/v1/workspaces/w1/api-keys`;

const toast = vi.hoisted(() => ({
	success: vi.fn(),
	error: vi.fn(),
}));

vi.mock('sonner', () => ({ toast }));

const workspace: Workspace = {
	id: 'w1',
	name: 'Acme',
	slug: 'acme',
	role: 'admin',
};

const keys: APIKey[] = [
	{
		id: 'k1',
		name: 'CI deploy',
		prefix: 'o1_abcdefghijkl',
		role: 'member',
		created_by: 'u1',
		created_at: '2026-11-09T09:00:00Z',
		last_used_at: null,
	},
	{
		id: 'k2',
		name: 'Read-only dashboard',
		prefix: 'o1_mnopqrstuvwx',
		role: 'viewer',
		created_by: 'u1',
		created_at: '2026-11-01T09:00:00Z',
		last_used_at: '2026-11-09T10:00:00Z',
	},
];

function serveKeys(list: APIKey[] = keys) {
	server.use(http.get(keysPath, () => HttpResponse.json({ keys: list })));
}

beforeEach(() => {
	toast.success.mockReset();
	toast.error.mockReset();
});

describe('APIKeysScreen', () => {
	test('renders the key table with headers, rows, and Never used', async () => {
		serveKeys();

		renderWithProviders(<APIKeysScreen workspace={workspace} />);

		expect(
			await screen.findByRole('heading', { name: 'API keys' }),
		).toBeInTheDocument();
		expect(
			screen.getAllByRole('columnheader').map((header) => header.textContent),
		).toEqual(['Name', 'Prefix', 'Role', 'Last used', 'Created', 'Actions']);

		const ciRow = screen.getByRole('row', { name: /CI deploy/ });
		expect(within(ciRow).getByText('Never used')).toBeInTheDocument();
		expect(screen.getByText('Read-only dashboard')).toBeInTheDocument();
		expect(screen.getByText('o1_abcdefghijkl')).toHaveClass('font-mono');
		expect(screen.getAllByRole('row')).toHaveLength(keys.length + 1);
	});

	test('Create key opens the create dialog', async () => {
		serveKeys();
		const user = userEvent.setup();

		renderWithProviders(<APIKeysScreen workspace={workspace} />);
		await screen.findByText('CI deploy');

		await user.click(screen.getByRole('button', { name: 'Create key' }));

		expect(
			await screen.findByRole('heading', { name: 'Create API key' }),
		).toBeInTheDocument();
		expect(screen.getByLabelText('Name')).toBeInTheDocument();
	});

	test('revoke confirms and calls DELETE with a success toast', async () => {
		serveKeys();
		const deletions: string[] = [];
		server.use(
			http.delete(`${keysPath}/k1`, () => {
				deletions.push('k1');
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<APIKeysScreen workspace={workspace} />);
		await screen.findByText('CI deploy');

		await user.click(screen.getByRole('button', { name: 'Revoke CI deploy' }));

		expect(
			await screen.findByText(/stop working immediately/i),
		).toBeInTheDocument();
		expect(screen.getByText(/audit history is retained/i)).toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'Revoke key' }));

		await waitFor(() => expect(deletions).toEqual(['k1']));
		expect(toast.success).toHaveBeenCalledWith('API key revoked');
	});

	test('cancelling revoke does nothing', async () => {
		serveKeys();
		const deletions: string[] = [];
		server.use(
			http.delete(`${keysPath}/k1`, () => {
				deletions.push('k1');
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<APIKeysScreen workspace={workspace} />);
		await screen.findByText('CI deploy');

		await user.click(screen.getByRole('button', { name: 'Revoke CI deploy' }));
		await screen.findByText(/stop working immediately/i);
		await user.click(screen.getByRole('button', { name: 'Cancel' }));

		await waitFor(() =>
			expect(
				screen.queryByText(/stop working immediately/i),
			).not.toBeInTheDocument(),
		);
		expect(deletions).toEqual([]);
		expect(toast.success).not.toHaveBeenCalled();
	});

	test('renders the empty state when there are no keys', async () => {
		serveKeys([]);

		renderWithProviders(<APIKeysScreen workspace={workspace} />);

		expect(await screen.findByText('No API keys yet.')).toBeInTheDocument();
	});

	test('never renders a secret or token hash', async () => {
		const secret = 'o1_abcdefghijkl_supersecretvalue';
		const tokenHash = '0123456789abcdef0123456789abcdef';
		const leaky = {
			...keys[0],
			secret,
			token_hash: tokenHash,
		} as APIKey;
		serveKeys([leaky]);

		renderWithProviders(<APIKeysScreen workspace={workspace} />);
		await screen.findByText('CI deploy');

		expect(document.body.textContent).not.toContain(secret);
		expect(document.body.textContent).not.toContain(tokenHash);
	});
});
