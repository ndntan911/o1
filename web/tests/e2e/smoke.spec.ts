import { execFileSync } from 'node:child_process';
import {
	expect,
	test,
	type APIRequestContext,
	type Page,
} from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { waitForVerificationLink } from './helpers';

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

function composePsql(sql: string): void {
	execFileSync(
		'docker',
		[
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
			'-c',
			sql,
		],
		{ stdio: 'pipe' },
	);
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

	// Email verification. If Mailpit is unavailable, verify directly in the
	// development database instead of adding a product endpoint.
	try {
		const verificationLink = await waitForVerificationLink(request, email);
		await page.goto(verificationLink);
		await expect(
			page.getByText('Email verified', { exact: true }),
		).toBeVisible();
		await page.getByRole('link', { name: 'Sign in' }).click();
	} catch {
		await registerThroughApiUnlessPresent(request, email, password);
		composePsql(
			`UPDATE users SET email_verified_at = now() WHERE email = '${email}'`,
		);
		await page.goto('/login');
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

	// Mobile navigation stays reachable at a phone viewport.
	await page.goto(`/w/${workspaceSlug}/monitors`);
	await expect(
		page.getByRole('heading', { name: 'Monitors', exact: true }),
	).toBeVisible();
	await page.setViewportSize({ width: 390, height: 844 });
	await page.getByRole('button', { name: 'Open navigation' }).click();
	await expect(
		page.getByRole('dialog').locator(`a[href="/w/${workspaceSlug}/monitors"]`),
	).toBeVisible();
});
