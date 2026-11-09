import { beforeEach, describe, expect, test, vi } from 'vitest';
import { useState } from 'react';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, delay, http } from 'msw';
import { server } from '@/test/msw/server';
import { createTestQueryClient, renderWithProviders } from '@/test/render';
import type { APIKey, CreatedAPIKey } from '@/lib/api/types';
import { CreateAPIKeyDialog } from './CreateAPIKeyDialog';

Element.prototype.hasPointerCapture = () => false;
Element.prototype.setPointerCapture = () => undefined;
Element.prototype.releasePointerCapture = () => undefined;
Element.prototype.scrollIntoView = () => undefined;

const ORIGIN = 'http://localhost:5173';
const keysPath = `${ORIGIN}/api/v1/workspaces/w1/api-keys`;

const toast = vi.hoisted(() => ({
	success: vi.fn(),
	error: vi.fn(),
}));

vi.mock('sonner', () => ({ toast }));

const key: APIKey = {
	id: 'k1',
	name: 'CI',
	prefix: 'o1_abcdefghijkl',
	role: 'member',
	created_by: 'u1',
	created_at: '2026-11-09T09:00:00Z',
	last_used_at: null,
};

const created: CreatedAPIKey = {
	key,
	secret: 'o1_abcdefghijkl_secretonetime',
};

function serveCreate(response: CreatedAPIKey = created) {
	const bodies: unknown[] = [];
	server.use(
		http.post(keysPath, async ({ request }) => {
			bodies.push(await request.json());
			return HttpResponse.json(response, { status: 201 });
		}),
	);
	return bodies;
}

function problem(code: string, status: number, title: string, detail?: string) {
	return HttpResponse.json(
		{ type: 'about:blank', title, status, code, detail },
		{ status, headers: { 'Content-Type': 'application/problem+json' } },
	);
}

function mockClipboard(writeText: ReturnType<typeof vi.fn>) {
	Object.defineProperty(navigator, 'clipboard', {
		configurable: true,
		value: { writeText },
	});
}

function CloseableDialog() {
	const [open, setOpen] = useState(true);
	return open ? (
		<CreateAPIKeyDialog workspaceId="w1" onClose={() => setOpen(false)} />
	) : (
		<button type="button" onClick={() => setOpen(true)}>
			Open dialog
		</button>
	);
}

function mutationCacheContains(
	queryClient: ReturnType<typeof createTestQueryClient>,
	secret: string,
) {
	return queryClient
		.getMutationCache()
		.getAll()
		.some((mutation) =>
			JSON.stringify(mutation.state.data ?? '').includes(secret),
		);
}

beforeEach(() => {
	toast.success.mockReset();
	toast.error.mockReset();
});

describe('CreateAPIKeyDialog', () => {
	test('renders Name, Role with Viewer default, and Create key', () => {
		renderWithProviders(
			<CreateAPIKeyDialog workspaceId="w1" onClose={vi.fn()} />,
		);

		expect(screen.getByLabelText('Name')).toBeInTheDocument();
		expect(screen.getByRole('combobox', { name: 'Role' })).toHaveTextContent(
			'Viewer',
		);
		expect(
			screen.getByRole('button', { name: 'Create key' }),
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument();
	});

	test('switching to Member submits the chosen role', async () => {
		const bodies = serveCreate();
		const user = userEvent.setup();

		renderWithProviders(
			<CreateAPIKeyDialog workspaceId="w1" onClose={vi.fn()} />,
		);

		await user.type(screen.getByLabelText('Name'), 'CI');
		await user.click(screen.getByRole('combobox', { name: 'Role' }));
		await user.click(await screen.findByRole('option', { name: 'Member' }));
		await user.click(screen.getByRole('button', { name: 'Create key' }));

		await waitFor(() =>
			expect(bodies).toEqual([{ name: 'CI', role: 'member' }]),
		);
	});

	test('shows the one-time secret and copies it', async () => {
		serveCreate();
		const user = userEvent.setup();
		const writeText = vi.fn().mockResolvedValue(undefined);
		mockClipboard(writeText);

		renderWithProviders(
			<CreateAPIKeyDialog workspaceId="w1" onClose={vi.fn()} />,
		);

		await user.type(screen.getByLabelText('Name'), 'CI');
		await user.click(screen.getByRole('button', { name: 'Create key' }));

		const secret = await screen.findByDisplayValue(created.secret);
		expect(secret).toHaveAttribute('readonly');
		expect(screen.getByText(/never be shown again/i)).toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'Copy key' }));

		expect(writeText).toHaveBeenCalledWith(created.secret);
		expect(toast.success).toHaveBeenCalledWith('API key copied');
	});

	test('focuses the secret and announces the warning after success', async () => {
		serveCreate();
		const user = userEvent.setup();

		renderWithProviders(
			<CreateAPIKeyDialog workspaceId="w1" onClose={vi.fn()} />,
		);

		await user.type(screen.getByLabelText('Name'), 'CI');
		await user.click(screen.getByRole('button', { name: 'Create key' }));

		const secret = await screen.findByDisplayValue(created.secret);
		await waitFor(() => expect(secret).toHaveFocus());

		const warning = screen.getByRole('status');
		expect(warning).toHaveTextContent(/never be shown again/i);
		expect(warning).toHaveAttribute('aria-live', 'polite');
	});

	test('ignores dismissal while the create is pending', async () => {
		let releaseRequest: () => void = () => undefined;
		const gate = new Promise<void>((resolve) => {
			releaseRequest = resolve;
		});
		server.use(
			http.post(keysPath, async () => {
				await gate;
				return HttpResponse.json(created, { status: 201 });
			}),
		);
		const onClose = vi.fn();
		const user = userEvent.setup();

		renderWithProviders(
			<CreateAPIKeyDialog workspaceId="w1" onClose={onClose} />,
		);

		await user.type(screen.getByLabelText('Name'), 'CI');
		await user.click(screen.getByRole('button', { name: 'Create key' }));
		await screen.findByRole('button', { name: 'Creating…' });

		await user.keyboard('{Escape}');
		expect(onClose).not.toHaveBeenCalled();
		expect(
			screen.getByRole('heading', { name: 'Create API key' }),
		).toBeInTheDocument();

		releaseRequest();
		expect(await screen.findByDisplayValue(created.secret)).toBeInTheDocument();
	});

	test('a failed copy shows the error toast', async () => {
		serveCreate();
		const user = userEvent.setup();
		const writeText = vi.fn().mockRejectedValue(new Error('denied'));
		mockClipboard(writeText);

		renderWithProviders(
			<CreateAPIKeyDialog workspaceId="w1" onClose={vi.fn()} />,
		);

		await user.type(screen.getByLabelText('Name'), 'CI');
		await user.click(screen.getByRole('button', { name: 'Create key' }));
		await screen.findByDisplayValue(created.secret);
		await user.click(screen.getByRole('button', { name: 'Copy key' }));

		expect(toast.error).toHaveBeenCalledWith('Could not copy the key');
	});

	test('closing and reopening never shows the secret again', async () => {
		serveCreate();
		const user = userEvent.setup();

		renderWithProviders(<CloseableDialog />);

		await user.type(screen.getByLabelText('Name'), 'CI');
		await user.click(screen.getByRole('button', { name: 'Create key' }));
		await screen.findByDisplayValue(created.secret);

		await user.click(screen.getByRole('button', { name: 'Done' }));
		expect(screen.queryByDisplayValue(created.secret)).not.toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'Open dialog' }));

		expect(screen.queryByDisplayValue(created.secret)).not.toBeInTheDocument();
		expect(screen.queryByText(/never be shown again/i)).not.toBeInTheDocument();
		expect(screen.getByLabelText('Name')).toHaveValue('');
	});

	test('closing the dialog evicts the settled mutation from the cache', async () => {
		serveCreate();
		const queryClient = createTestQueryClient();
		const user = userEvent.setup();

		renderWithProviders(<CloseableDialog />, { queryClient });

		await user.type(screen.getByLabelText('Name'), 'CI');
		await user.click(screen.getByRole('button', { name: 'Create key' }));
		await screen.findByDisplayValue(created.secret);

		expect(mutationCacheContains(queryClient, created.secret)).toBe(false);

		await user.click(screen.getByRole('button', { name: 'Done' }));

		expect(mutationCacheContains(queryClient, created.secret)).toBe(false);
	});

	test('unmounting mid-flight evicts the pending mutation from the cache', async () => {
		let releaseRequest: () => void = () => undefined;
		const gate = new Promise<void>((resolve) => {
			releaseRequest = resolve;
		});
		let requestFinished = false;
		server.use(
			http.post(keysPath, async () => {
				await gate;
				await delay(10);
				requestFinished = true;
				return HttpResponse.json(created, { status: 201 });
			}),
		);
		const queryClient = createTestQueryClient();
		const user = userEvent.setup();

		const { unmount } = renderWithProviders(
			<CreateAPIKeyDialog workspaceId="w1" onClose={vi.fn()} />,
			{ queryClient },
		);

		await user.type(screen.getByLabelText('Name'), 'CI');
		await user.click(screen.getByRole('button', { name: 'Create key' }));

		await waitFor(() =>
			expect(queryClient.getMutationCache().getAll()).toHaveLength(1),
		);

		unmount();
		expect(queryClient.getMutationCache().getAll()).toHaveLength(0);

		releaseRequest();
		await waitFor(() => expect(requestFinished).toBe(true));
		expect(mutationCacheContains(queryClient, created.secret)).toBe(false);
	});

	test('a 409 problem stays open and shows the problem', async () => {
		server.use(
			http.post(keysPath, () =>
				problem(
					'api_key_name_taken',
					409,
					'API Key Name Taken',
					'An active key already uses that name.',
				),
			),
		);
		const onClose = vi.fn();
		const user = userEvent.setup();

		renderWithProviders(
			<CreateAPIKeyDialog workspaceId="w1" onClose={onClose} />,
		);

		await user.type(screen.getByLabelText('Name'), 'CI');
		await user.click(screen.getByRole('button', { name: 'Create key' }));

		expect(await screen.findByText('API Key Name Taken')).toBeInTheDocument();
		expect(
			screen.getByText('An active key already uses that name.'),
		).toBeInTheDocument();
		expect(screen.getByText('Code: api_key_name_taken')).toBeInTheDocument();
		expect(
			screen.getByRole('heading', { name: 'Create API key' }),
		).toBeInTheDocument();
		expect(onClose).not.toHaveBeenCalled();
	});
});
