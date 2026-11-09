import { beforeEach, describe, expect, test, vi } from 'vitest';
import { useState } from 'react';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { createTestQueryClient, renderWithProviders } from '@/test/render';
import { EnableTOTPDialog } from './EnableTOTPDialog';

const ORIGIN = 'http://localhost:5173';
const base = `${ORIGIN}/api/v1/auth/2fa`;

const toast = vi.hoisted(() => ({
	success: vi.fn(),
	error: vi.fn(),
}));

vi.mock('sonner', () => ({ toast }));

const setup = {
	secret: 'JBSWY3DPEHPK3PXP',
	otpauth_uri: 'otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP',
};

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

function serveSetup() {
	const bodies: unknown[] = [];
	server.use(
		http.post(`${base}/setup`, async ({ request }) => {
			bodies.push(await request.json());
			return HttpResponse.json(setup);
		}),
	);
	return bodies;
}

function serveEnable(codes = recoveryCodes) {
	const bodies: unknown[] = [];
	server.use(
		http.post(`${base}/enable`, async ({ request }) => {
			bodies.push(await request.json());
			return HttpResponse.json({ recovery_codes: codes });
		}),
	);
	return bodies;
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
		<EnableTOTPDialog onClose={() => setOpen(false)} />
	) : (
		<button type="button" onClick={() => setOpen(true)}>
			Open dialog
		</button>
	);
}

async function startSetup(
	user: ReturnType<typeof userEvent.setup>,
	password = 'correct horse battery',
) {
	await user.type(screen.getByLabelText('Password'), password);
	await user.click(screen.getByRole('button', { name: 'Continue' }));
}

beforeEach(() => {
	toast.success.mockReset();
	toast.error.mockReset();
});

describe('EnableTOTPDialog', () => {
	test('asks for the password, then shows the QR, secret, password, and code fields', async () => {
		const setupBodies = serveSetup();
		const user = userEvent.setup();

		renderWithProviders(<EnableTOTPDialog onClose={vi.fn()} />);

		expect(
			screen.getByRole('heading', {
				name: 'Enable two-factor authentication',
			}),
		).toBeInTheDocument();
		expect(screen.getByLabelText('Password')).toBeInTheDocument();
		expect(screen.queryByTestId('totp-qr')).not.toBeInTheDocument();

		await startSetup(user);

		expect(await screen.findByTestId('totp-qr')).toBeInTheDocument();
		expect(screen.getByText(setup.secret)).toBeInTheDocument();
		expect(setupBodies).toEqual([{ password: 'correct horse battery' }]);
		expect(screen.getByLabelText('Password')).toBeInTheDocument();
		expect(screen.getByLabelText('Verification code')).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Verify and enable' }),
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument();
	});

	test('enabling shows ten recovery codes once and copies them', async () => {
		serveSetup();
		const enableBodies = serveEnable();
		const user = userEvent.setup();
		const writeText = vi.fn().mockResolvedValue(undefined);
		mockClipboard(writeText);
		const queryClient = createTestQueryClient();

		renderWithProviders(<EnableTOTPDialog onClose={vi.fn()} />, {
			queryClient,
		});
		await startSetup(user);

		await screen.findByTestId('totp-qr');
		await user.type(screen.getByLabelText('Password'), 'correct horse battery');
		await user.type(screen.getByLabelText('Verification code'), '123456');
		await user.click(screen.getByRole('button', { name: 'Verify and enable' }));

		await waitFor(() =>
			expect(enableBodies).toEqual([
				{ password: 'correct horse battery', code: '123456' },
			]),
		);
		for (const code of recoveryCodes) {
			expect(await screen.findByText(code)).toBeInTheDocument();
		}
		expect(screen.getByText(/never be shown again/i)).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Copy codes' }),
		).toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'Copy codes' }));

		expect(writeText).toHaveBeenCalledWith(recoveryCodes.join('\n'));
		expect(toast.success).toHaveBeenCalledWith('Recovery codes copied');

		const cached = JSON.stringify(
			queryClient
				.getMutationCache()
				.getAll()
				.map((mutation) => [
					mutation.state.variables,
					mutation.state.data ?? null,
				]),
		);
		expect(cached).not.toContain(recoveryCodes[0]);
		expect(cached).not.toContain(setup.secret);
		expect(cached).not.toContain('correct horse battery');
	});

	test('a wrong password shows a ProblemAlert and no QR code', async () => {
		server.use(
			http.post(`${base}/setup`, () =>
				problem('invalid_credentials', 401, 'Invalid password'),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<EnableTOTPDialog onClose={vi.fn()} />);
		await startSetup(user, 'wrong password');

		expect(await screen.findByText('Invalid password')).toBeInTheDocument();
		expect(screen.getByText(/invalid_credentials/)).toBeInTheDocument();
		expect(screen.queryByTestId('totp-qr')).not.toBeInTheDocument();
	});

	test('closing after enabling hides the QR, secret, and codes', async () => {
		serveSetup();
		serveEnable();
		const user = userEvent.setup();

		renderWithProviders(<CloseableDialog />);
		await startSetup(user);

		await screen.findByTestId('totp-qr');
		await user.type(screen.getByLabelText('Password'), 'correct horse battery');
		await user.type(screen.getByLabelText('Verification code'), '123456');
		await user.click(screen.getByRole('button', { name: 'Verify and enable' }));
		await screen.findByText(recoveryCodes[0]);

		await user.click(screen.getByRole('button', { name: 'Done' }));

		expect(screen.queryByText(recoveryCodes[0])).not.toBeInTheDocument();
		expect(screen.queryByTestId('totp-qr')).not.toBeInTheDocument();
		expect(screen.queryByText(setup.secret)).not.toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'Open dialog' }));

		expect(screen.getByLabelText('Password')).toBeInTheDocument();
		expect(screen.queryByTestId('totp-qr')).not.toBeInTheDocument();
		expect(screen.queryByText(setup.secret)).not.toBeInTheDocument();
		expect(
			screen.queryByRole('button', { name: 'Copy codes' }),
		).not.toBeInTheDocument();
	});
});
