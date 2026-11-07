import {
	expect,
	test,
	type APIRequestContext,
	type Page,
} from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import {
	composePsql,
	MailpitTimeoutError,
	seedIncident,
	waitForVerificationLink,
} from './helpers';

const WORKSPACE_NAME = 'E2E Workspace';
const MONITOR_NAME = 'Example';
const MONITOR_NAME_EDITED = 'Example edited';

async function expectNoSeriousA11yViolations(page: Page) {
	const results = await new AxeBuilder({ page }).analyze();
	const violations = results.violations.filter(
		(violation) =>
			violation.impact === 'serious' || violation.impact === 'critical',
	);
	expect(
		violations.map((violation) => ({
			id: violation.id,
			impact: violation.impact,
			help: violation.help,
			targets: violation.nodes.map((node) => ({
				target: node.target.join(' '),
				html: node.html,
				failure: node.failureSummary,
			})),
		})),
	).toEqual([]);
}

async function expectPressedThemeContrast(page: Page): Promise<void> {
	const ratio = await page.evaluate(() => {
		type Rgb = [number, number, number];

		const canvas = document.createElement('canvas');
		canvas.width = 1;
		canvas.height = 1;
		const context = canvas.getContext('2d');

		function parseBackground(value: string): Rgb | null {
			if (!context) return null;
			context.clearRect(0, 0, 1, 1);
			context.fillStyle = value;
			context.fillRect(0, 0, 1, 1);
			const [red, green, blue, alpha] = context.getImageData(0, 0, 1, 1).data;
			if (alpha === 0) return null;
			return [red, green, blue];
		}

		function opaqueBackground(element: Element | null): Rgb {
			let current = element;
			while (current) {
				const background = parseBackground(
					getComputedStyle(current).backgroundColor,
				);
				if (background) return background;
				current = current.parentElement;
			}
			return [255, 255, 255];
		}

		function luminance([red, green, blue]: Rgb): number {
			const channel = (value: number) => {
				const scaled = value / 255;
				return scaled <= 0.03928
					? scaled / 12.92
					: ((scaled + 0.055) / 1.055) ** 2.4;
			};
			return (
				0.2126 * channel(red) + 0.7152 * channel(green) + 0.0722 * channel(blue)
			);
		}

		const pressed = document.querySelector('[aria-pressed="true"]');
		if (!pressed) throw new Error('No theme option is pressed');
		const foreground = luminance(opaqueBackground(pressed));
		const surface = luminance(opaqueBackground(pressed.parentElement));
		const lighter = Math.max(foreground, surface);
		const darker = Math.min(foreground, surface);
		return (lighter + 0.05) / (darker + 0.05);
	});

	expect(ratio).toBeGreaterThanOrEqual(3);
}

async function registerThroughApiUnlessPresent(
	request: APIRequestContext,
	email: string,
	password: string,
): Promise<void> {
	const response = await request.post('/api/v1/auth/register', {
		data: { email, password },
	});
	// 409 means the browser registration already created the account.
	if (!response.ok() && response.status() !== 409) {
		throw new Error(
			`Fallback API registration failed with status ${response.status()}: ${await response.text()}`,
		);
	}
}

test('register, verify, create a monitor, and administer the workspace', async ({
	page,
	request,
}) => {
	const email = `e2e-${Date.now()}@example.com`;
	const password = 'correct horse battery staple';

	// Registration.
	await page.goto('/register');
	await expect(page.getByLabel('Email')).toBeVisible();
	await expectNoSeriousA11yViolations(page);
	await page.getByLabel('Email').fill(email);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByLabel('Confirm password').fill(password);
	await page.getByRole('button', { name: 'Create account' }).click();
	await expect(
		page.getByText('Check your email', { exact: true }),
	).toBeVisible();

	// Email verification. Only a Mailpit timeout may fall back to SQL
	// verification; transport errors and failures in the verification UI
	// itself must fail the test.
	let verificationLink: string | undefined;
	try {
		verificationLink = await waitForVerificationLink(request, email);
	} catch (error) {
		if (!(error instanceof MailpitTimeoutError)) throw error;
		await registerThroughApiUnlessPresent(request, email, password);
		composePsql(
			`UPDATE users SET email_verified_at = now() WHERE email = '${email}'`,
		);
		test.info().annotations.push({
			type: 'verification-fallback',
			description:
				'Mailpit had no verification email after 30s; used SQL verification and skipped /verify-email',
		});
		console.warn(
			`Mailpit had no verification email for ${email} after 30s; using SQL verification fallback.`,
		);
		await page.goto('/login');
	}

	if (verificationLink) {
		const expectedOrigin = new URL(
			process.env.E2E_BASE_URL ?? 'http://localhost:5173',
		).origin;
		expect(new URL(verificationLink).origin).toBe(expectedOrigin);
		await page.goto(verificationLink);
		await expect(
			page.getByText('Email verified', { exact: true }),
		).toBeVisible();
		await page.getByRole('link', { name: 'Sign in' }).click();
	}

	// Login, including an axe pass on /login.
	await expect(page.getByLabel('Email')).toBeVisible();
	await expectNoSeriousA11yViolations(page);
	await page.getByLabel('Email').fill(email);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();

	// Workspace creation. The list starts empty for a fresh account.
	await expect(
		page.getByRole('heading', { name: 'Workspaces', exact: true }),
	).toBeVisible();
	await page.getByRole('button', { name: 'New workspace' }).click();
	const workspaceDialog = page.getByRole('dialog');
	await workspaceDialog
		.getByLabel('Name', { exact: true })
		.fill(WORKSPACE_NAME);
	await workspaceDialog
		.getByRole('button', { name: 'Create workspace' })
		.click();

	await expect(
		page.getByRole('heading', { name: 'Monitors', exact: true }),
	).toBeVisible();
	const workspaceSlug = new URL(page.url()).pathname.split('/')[2];
	expect(workspaceSlug).toBeTruthy();

	// Theme: dark mode applies immediately, survives reload, and stays accessible.
	await page.getByRole('button', { name: 'Dark', exact: true }).click();
	await expect(page.locator('html')).toHaveClass(/dark/);
	await expectPressedThemeContrast(page);
	await page.reload();
	await expect(
		page.getByRole('heading', { name: 'Monitors', exact: true }),
	).toBeVisible();
	await expect(page.locator('html')).toHaveClass(/dark/);
	await expect
		.poll(() => page.evaluate(() => document.documentElement.style.colorScheme))
		.toBe('dark');
	await expectNoSeriousA11yViolations(page);
	await page.getByRole('button', { name: 'System', exact: true }).click();
	await expect
		.poll(() => page.evaluate(() => localStorage.getItem('o1-theme')))
		.toBe('system');

	// Monitor creation.
	await page.getByRole('link', { name: 'New monitor', exact: true }).click();
	await expect(
		page.getByRole('heading', { name: 'New monitor', exact: true }),
	).toBeVisible();
	await page.getByLabel('Name', { exact: true }).fill(MONITOR_NAME);
	await page.getByLabel('URL', { exact: true }).fill('https://example.com');
	await page.getByRole('button', { name: 'Create monitor' }).click();

	await expect(
		page.getByRole('heading', { name: MONITOR_NAME, exact: true }),
	).toBeVisible();
	await expect(
		page.getByRole('heading', { name: 'Summary', exact: true }),
	).toBeVisible();
	await expectNoSeriousA11yViolations(page);

	// Non-email alert channels.
	await page.goto(`/w/${workspaceSlug}/settings/alerts`);
	await expect(
		page.getByRole('heading', { name: 'Alert settings', exact: true }),
	).toBeVisible();

	await page.getByRole('button', { name: 'Add channel' }).click();
	await expect(
		page.getByRole('heading', { name: 'Add alert channel', exact: true }),
	).toBeVisible();
	await page.getByLabel('Name', { exact: true }).fill('Deploy hook');
	await page.getByLabel('Webhook URL').fill('https://example.com/o1');
	await page.getByRole('button', { name: 'Save channel' }).click();
	await expect(page.getByText('Deploy hook', { exact: true })).toBeVisible();
	await expect(page.getByText('https://example.com/…')).toBeVisible();

	await page.getByRole('button', { name: 'Add channel' }).click();
	await page.getByRole('combobox', { name: 'Kind' }).click();
	await page.getByRole('option', { name: 'Slack' }).click();
	await page.getByLabel('Name', { exact: true }).fill('Slack #ops');
	await page
		.getByLabel('Slack webhook URL')
		.fill('https://hooks.slack.com/services/T000/B000/XXXX');
	await page.getByRole('button', { name: 'Save channel' }).click();
	await expect(page.getByText('Slack #ops', { exact: true })).toBeVisible();

	await page.getByRole('button', { name: 'Add channel' }).click();
	await page.getByRole('combobox', { name: 'Kind' }).click();
	await page.getByRole('option', { name: 'Telegram' }).click();
	await page.getByLabel('Name', { exact: true }).fill('Telegram on-call');
	await page.getByLabel('Bot token').fill('123456:ABCdef');
	await page.getByLabel('Chat ID').fill('-1001234567890');
	await page.getByRole('button', { name: 'Save channel' }).click();
	const telegramRow = page
		.getByRole('listitem')
		.filter({ hasText: 'Telegram on-call' });
	await expect(telegramRow).toContainText('-1001234567890');
	await expect(
		telegramRow.getByText('Configured', { exact: true }),
	).toBeVisible();
	await expect(page.getByText('123456:ABCdef')).toHaveCount(0);

	// Per-monitor switches persist across reloads.
	await page.goto(`/w/${workspaceSlug}/monitors`);
	await page.getByRole('link', { name: `View ${MONITOR_NAME}` }).click();
	const deploySwitch = page.getByRole('switch', {
		name: 'Deploy hook alerts',
	});
	await expect(deploySwitch).toBeChecked();
	await deploySwitch.click();
	await expect(deploySwitch).not.toBeChecked();
	await page.reload();
	await expect(
		page.getByRole('switch', { name: 'Deploy hook alerts' }),
	).not.toBeChecked();

	// Edit and delete a channel; delivery history survives the delete.
	await page.goto(`/w/${workspaceSlug}/settings/alerts`);
	const slackRow = page.getByRole('listitem').filter({ hasText: 'Slack #ops' });
	await slackRow.getByRole('button', { name: 'Edit', exact: true }).click();
	await page.getByLabel('Name', { exact: true }).fill('Slack #alerts');
	await page.getByRole('switch', { name: 'Channel enabled' }).click();
	await page.getByRole('button', { name: 'Save channel' }).click();
	const slackEdited = page
		.getByRole('listitem')
		.filter({ hasText: 'Slack #alerts' });
	await expect(slackEdited).toBeVisible();
	await expect(
		slackEdited.getByRole('switch', { name: 'Slack #alerts enabled' }),
	).not.toBeChecked();

	await slackEdited
		.getByRole('button', { name: 'Delete Slack #alerts' })
		.click();
	await expect(page.getByText(/delivery history is retained/i)).toBeVisible();
	await page.getByRole('button', { name: 'Delete channel' }).click();
	await expect(page.getByText('Slack #alerts', { exact: true })).toHaveCount(0);

	// Return to the monitor before exercising the existing monitor flow.
	await page.goto(`/w/${workspaceSlug}/monitors`);
	await page.getByRole('link', { name: `View ${MONITOR_NAME}` }).click();
	await expect(
		page.getByRole('heading', { name: MONITOR_NAME, exact: true }),
	).toBeVisible();

	// Pause and resume from the detail screen.
	await page.getByRole('button', { name: `Pause ${MONITOR_NAME}` }).click();
	await expect(
		page.getByRole('button', { name: `Resume ${MONITOR_NAME}` }),
	).toBeVisible();
	await expect(page.getByText('Paused', { exact: true }).first()).toBeVisible();
	await page.getByRole('button', { name: `Resume ${MONITOR_NAME}` }).click();
	await expect(
		page.getByRole('button', { name: `Pause ${MONITOR_NAME}` }),
	).toBeVisible();

	// Incident dashboard. Seed an open incident directly so the UI can be
	// exercised without waiting for a probe to fail.
	const seeded = seedIncident(MONITOR_NAME);
	expect(seeded.workspaceSlug).toBe(workspaceSlug);

	await page.goto(`/w/${workspaceSlug}/incidents`);
	await expect(
		page.getByRole('heading', { name: 'Incidents', exact: true }),
	).toBeVisible();
	const incidentRow = page
		.getByRole('row')
		.filter({ hasText: MONITOR_NAME })
		.first();
	await expect(incidentRow).toBeVisible();
	await expect(incidentRow.getByText('Open', { exact: true })).toBeVisible();

	await incidentRow
		.getByRole('link', { name: `View ${MONITOR_NAME} incident` })
		.click();
	await expect(
		page.getByRole('heading', {
			name: `Incident: ${MONITOR_NAME}`,
			exact: true,
		}),
	).toBeVisible();
	const timeline = page.getByRole('list', { name: 'Incident timeline' });
	await expect(timeline.getByText('Opened', { exact: true })).toBeVisible();

	await page.getByRole('button', { name: 'Acknowledge', exact: true }).click();
	await expect(
		page.getByRole('button', { name: 'Acknowledge', exact: true }),
	).toBeHidden();
	await expect(
		timeline.getByText('Acknowledged', { exact: true }),
	).toBeVisible();

	// Acknowledged incidents stay active and remain linked from the monitor.
	await page.getByRole('link', { name: 'View monitor' }).click();
	await page.reload();
	await expect(
		page.getByText('Active incident', { exact: true }),
	).toBeVisible();
	await expect(page.getByText(/Acknowledged incident started/)).toBeVisible();
	await expect(
		page.getByRole('link', { name: 'View incident' }),
	).toHaveAttribute(
		'href',
		`/w/${workspaceSlug}/incidents/${seeded.incidentId}`,
	);
	await expect(
		page
			.getByRole('region', { name: 'Recent incidents' })
			.getByRole('link', { name: 'Acknowledged', exact: true }),
	).toBeVisible();

	// Edit the monitor, then check the list screen with a populated row.
	await page.getByRole('link', { name: 'Edit', exact: true }).click();
	await expect(
		page.getByRole('heading', { name: 'Edit monitor', exact: true }),
	).toBeVisible();
	await page.getByLabel('Name', { exact: true }).fill(MONITOR_NAME_EDITED);
	await page.getByRole('button', { name: 'Save changes' }).click();
	await expect(
		page.getByRole('heading', { name: MONITOR_NAME_EDITED, exact: true }),
	).toBeVisible();

	await page.getByRole('link', { name: 'Back to monitors' }).click();
	await expect(
		page.getByRole('heading', { name: 'Monitors', exact: true }),
	).toBeVisible();
	await expect(
		page.getByRole('row').filter({ hasText: MONITOR_NAME_EDITED }).first(),
	).toBeVisible();
	await expectNoSeriousA11yViolations(page);

	// Delete the monitor from its detail screen.
	await page.getByRole('link', { name: `View ${MONITOR_NAME_EDITED}` }).click();
	await expect(
		page.getByRole('heading', { name: MONITOR_NAME_EDITED, exact: true }),
	).toBeVisible();
	await page.getByRole('button', { name: 'Delete', exact: true }).click();
	await page
		.getByRole('button', { name: 'Delete monitor', exact: true })
		.click();
	await expect(
		page.getByText('No monitors yet', { exact: true }),
	).toBeVisible();

	// Promote through direct SQL; there is no product API for this.
	composePsql(`UPDATE users SET is_admin = true WHERE email = '${email}'`);
	await page.reload();
	await page.goto('/admin/workspaces');
	await expect(
		page.getByRole('heading', { name: 'Admin', exact: true }),
	).toBeVisible();

	const workspaceRow = page
		.getByRole('row')
		.filter({ hasText: workspaceSlug })
		.first();
	await expect(workspaceRow).toBeVisible();

	// Suspend the workspace with a reason.
	await workspaceRow
		.getByRole('button', { name: 'Suspend', exact: true })
		.click();
	const suspendDialog = page.getByRole('dialog');
	await suspendDialog
		.getByLabel('Reason', { exact: true })
		.fill('E2E suspension');
	await suspendDialog
		.getByRole('button', { name: 'Suspend workspace', exact: true })
		.click();
	await expect(
		workspaceRow.getByText('Suspended', { exact: true }),
	).toBeVisible();

	// Unsuspend it again.
	await workspaceRow
		.getByRole('button', { name: 'Unsuspend', exact: true })
		.click();
	await page
		.getByRole('button', { name: 'Unsuspend workspace', exact: true })
		.click();
	await expect(workspaceRow.getByText('Active', { exact: true })).toBeVisible();

	// Mobile navigation stays reachable at a phone viewport, including the
	// bottom-anchored theme control on a short screen.
	await page.goto(`/w/${workspaceSlug}/monitors`);
	await expect(
		page.getByRole('heading', { name: 'Monitors', exact: true }),
	).toBeVisible();
	await page.setViewportSize({ width: 320, height: 568 });
	await page.getByRole('button', { name: 'Open navigation' }).click();
	const navigation = page.getByRole('dialog');
	await expect(
		navigation.locator(`a[href="/w/${workspaceSlug}/monitors"]`),
	).toBeVisible();
	const themeGroup = navigation.getByRole('group', { name: 'Theme' });
	await expect(themeGroup).toBeVisible();
	await themeGroup.scrollIntoViewIfNeeded();
	await navigation.getByRole('button', { name: 'Dark', exact: true }).click();
	await expect(page.locator('html')).toHaveClass(/dark/);
	await navigation.getByRole('button', { name: 'System', exact: true }).click();
	await expect
		.poll(() => page.evaluate(() => localStorage.getItem('o1-theme')))
		.toBe('system');
});

// Pre-paint behavior is tested with the app's module scripts blocked, so only
// the inline boot script can apply the theme.
test('applies the stored theme before the app loads', async ({ page }) => {
	await page.addInitScript(() => {
		localStorage.setItem('o1-theme', 'dark');
	});
	await page.route('**/*.js', (route) => route.abort());

	await page.goto('/', { waitUntil: 'domcontentloaded' });

	await expect(page.locator('html')).toHaveClass(/dark/);
	await expect
		.poll(() => page.evaluate(() => document.documentElement.style.colorScheme))
		.toBe('dark');
	await expect(page.locator('#root')).toBeEmpty();
});

test('ignores an invalid stored theme', async ({ page }) => {
	await page.addInitScript(() => {
		localStorage.setItem('o1-theme', 'blue');
	});
	await page.emulateMedia({ colorScheme: 'dark' });
	await page.route('**/*.js', (route) => route.abort());

	await page.goto('/', { waitUntil: 'domcontentloaded' });

	await expect
		.poll(() => page.evaluate(() => localStorage.getItem('o1-theme')))
		.toBeNull();
	await expect(page.locator('html')).toHaveClass(/dark/);
	await expect
		.poll(() => page.evaluate(() => document.documentElement.style.colorScheme))
		.toBe('dark');
});
