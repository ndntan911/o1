import { describe, expect, test } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { QueryClientProvider } from '@tanstack/react-query';
import {
	RouterProvider,
	createMemoryHistory,
	createRootRoute,
	createRoute,
	createRouter,
} from '@tanstack/react-router';
import { ThemeProvider } from 'next-themes';
import { server } from '@/test/msw/server';
import { createTestQueryClient, renderWithProviders } from '@/test/render';
import { UserMenu } from '@/components/layout/UserMenu';
import { SecurityScreen } from './SecurityScreen';

const ORIGIN = 'http://localhost:5173';
const base = `${ORIGIN}/api/v1/auth/2fa`;

const recoveryCodes = [
	'aaaaa-bbbbb',
	'ccccc-ddddd',
	'eeeee-fffff',
	'ggggg-hhhhh',
	'iiiii-jjjjj',
	'kkkkk-lllll',
	'mmmmm-nnnnn',
	'ooooo-ppppp',
	'qqqqq-rrrrr',
	'sssss-ttttt',
];

function problem(code: string, status: number, title: string) {
	return HttpResponse.json(
		{ type: 'about:blank', title, status, code },
		{ status, headers: { 'Content-Type': 'application/problem+json' } },
	);
}

function serveState(initial = { enabled: false, recovery_codes_remaining: 0 }) {
	let state = initial;
	server.use(http.get(base, () => HttpResponse.json(state)));
	return {
		set(next: typeof initial) {
			state = next;
		},
	};
}

async function openDisableDialog(user: ReturnType<typeof userEvent.setup>) {
	await user.click(
		await screen.findByRole('button', {
			name: 'Disable two-factor authentication',
		}),
	);
	return screen.getByRole('dialog');
}

async function armDisable(
	user: ReturnType<typeof userEvent.setup>,
	dialog: HTMLElement,
) {
	await user.type(within(dialog).getByLabelText('Password'), 'correct horse');
	await user.type(within(dialog).getByLabelText('Verification code'), '123456');
}

describe('SecurityScreen', () => {
	test('the disabled state explains two-factor and offers to enable it', async () => {
		serveState();

		renderWithProviders(<SecurityScreen />);

		expect(
			await screen.findByRole('heading', { name: 'Security' }),
		).toBeInTheDocument();
		expect(
			screen.getByRole('heading', { name: 'Two-factor authentication' }),
		).toBeInTheDocument();
		expect(screen.getByText(/authenticator app/i)).toBeInTheDocument();
		expect(
			screen.getByRole('button', {
				name: 'Enable two-factor authentication',
			}),
		).toBeInTheDocument();
	});

	test('opening enable shows the enable dialog', async () => {
		serveState();
		const user = userEvent.setup();

		renderWithProviders(<SecurityScreen />);
		await user.click(
			await screen.findByRole('button', {
				name: 'Enable two-factor authentication',
			}),
		);

		expect(
			screen.getByRole('heading', {
				name: 'Enable two-factor authentication',
			}),
		).toBeInTheDocument();
		expect(screen.getByLabelText('Password')).toBeInTheDocument();
	});

	test('the enabled state shows the remaining count and management actions', async () => {
		serveState({ enabled: true, recovery_codes_remaining: 8 });

		renderWithProviders(<SecurityScreen />);

		expect(
			await screen.findByText('Two-factor authentication is on'),
		).toBeInTheDocument();
		expect(screen.getByText('8 recovery codes remaining')).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Regenerate recovery codes' }),
		).toBeInTheDocument();
		expect(
			screen.getByRole('button', {
				name: 'Disable two-factor authentication',
			}),
		).toBeInTheDocument();
		expect(
			screen.queryByRole('button', {
				name: 'Enable two-factor authentication',
			}),
		).not.toBeInTheDocument();
	});

	test('the disable dialog needs a password and one factor, then disables', async () => {
		const state = serveState({ enabled: true, recovery_codes_remaining: 8 });
		const bodies: unknown[] = [];
		server.use(
			http.post(`${base}/disable`, async ({ request }) => {
				bodies.push(await request.json());
				state.set({ enabled: false, recovery_codes_remaining: 0 });
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<SecurityScreen />);
		const dialog = await openDisableDialog(user);

		const submit = within(dialog).getByRole('button', {
			name: 'Disable two-factor authentication',
		});
		expect(submit).toBeDisabled();

		await user.type(within(dialog).getByLabelText('Password'), 'correct horse');
		expect(submit).toBeDisabled();

		await user.type(
			within(dialog).getByLabelText('Verification code'),
			'123456',
		);
		expect(submit).toBeEnabled();
		await user.click(submit);

		await waitFor(() =>
			expect(bodies).toEqual([{ password: 'correct horse', code: '123456' }]),
		);
		expect(await screen.findByText(/authenticator app/i)).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Enable two-factor authentication' }),
		).toBeInTheDocument();
	});

	test('the disable dialog can verify a recovery code instead', async () => {
		const state = serveState({ enabled: true, recovery_codes_remaining: 8 });
		const bodies: unknown[] = [];
		server.use(
			http.post(`${base}/disable`, async ({ request }) => {
				bodies.push(await request.json());
				state.set({ enabled: false, recovery_codes_remaining: 0 });
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<SecurityScreen />);
		const dialog = await openDisableDialog(user);

		await user.click(within(dialog).getByLabelText('Use recovery code'));
		expect(within(dialog).getByLabelText('Recovery code')).toBeInTheDocument();

		await user.type(within(dialog).getByLabelText('Password'), 'correct horse');
		await user.type(
			within(dialog).getByLabelText('Recovery code'),
			'aaaaa-bbbbb',
		);
		await user.click(
			within(dialog).getByRole('button', {
				name: 'Disable two-factor authentication',
			}),
		);

		await waitFor(() =>
			expect(bodies).toEqual([
				{ password: 'correct horse', recovery_code: 'aaaaa-bbbbb' },
			]),
		);
	});

	test('regenerating shows new codes once and updates the count', async () => {
		const state = serveState({ enabled: true, recovery_codes_remaining: 2 });
		server.use(
			http.post(`${base}/recovery-codes`, () => {
				state.set({ enabled: true, recovery_codes_remaining: 10 });
				return HttpResponse.json({ recovery_codes: recoveryCodes });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<SecurityScreen />);
		await user.click(
			await screen.findByRole('button', {
				name: 'Regenerate recovery codes',
			}),
		);
		const dialog = screen.getByRole('dialog');
		await user.type(within(dialog).getByLabelText('Password'), 'correct horse');
		await user.type(
			within(dialog).getByLabelText('Verification code'),
			'123456',
		);
		await user.click(
			within(dialog).getByRole('button', {
				name: 'Regenerate recovery codes',
			}),
		);

		expect(await screen.findByText(recoveryCodes[0])).toBeInTheDocument();
		const codesDialog = screen.getByRole('dialog');
		expect(
			within(codesDialog).getByText(/never be shown again/i),
		).toBeInTheDocument();
		expect(
			await screen.findByText('10 recovery codes remaining'),
		).toBeInTheDocument();

		await user.click(within(codesDialog).getByRole('button', { name: 'Done' }));

		expect(screen.queryByText(recoveryCodes[0])).not.toBeInTheDocument();
	});

	test('a wrong password in the disable dialog renders a ProblemAlert', async () => {
		serveState({ enabled: true, recovery_codes_remaining: 8 });
		server.use(
			http.post(`${base}/disable`, () =>
				problem('invalid_credentials', 401, 'Invalid password'),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<SecurityScreen />);
		const dialog = await openDisableDialog(user);
		await armDisable(user, dialog);
		await user.click(
			within(dialog).getByRole('button', {
				name: 'Disable two-factor authentication',
			}),
		);

		expect(await screen.findByText('Invalid password')).toBeInTheDocument();
		expect(screen.getByText(/invalid_credentials/)).toBeInTheDocument();
		expect(screen.getByRole('dialog')).toBeInTheDocument();
	});

	test('the UserMenu links to the security page', async () => {
		const rootRoute = createRootRoute({
			component: () => <UserMenu email="alice@example.com" />,
		});
		const securityRoute = createRoute({
			getParentRoute: () => rootRoute,
			path: '/account/security',
			component: () => <p>security route</p>,
		});
		const router = createRouter({
			routeTree: rootRoute.addChildren([securityRoute]),
			history: createMemoryHistory({ initialEntries: ['/'] }),
		});

		render(
			<ThemeProvider
				attribute="class"
				defaultTheme="system"
				enableSystem
				storageKey="o1-theme"
			>
				<QueryClientProvider client={createTestQueryClient()}>
					<RouterProvider router={router} />
				</QueryClientProvider>
			</ThemeProvider>,
		);

		const link = await screen.findByRole('link', { name: 'Security' });
		expect(link).toHaveAttribute('href', '/account/security');
	});
});
