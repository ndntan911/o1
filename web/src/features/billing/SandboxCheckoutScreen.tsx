import { useMutation } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { Button } from '@/components/ui/button';
import type { Workspace } from '@/lib/api/types';
import { completeSandbox } from './api';

function planFromToken(token: string): string {
	try {
		const payload = token.split('.')[0].replace(/-/g, '+').replace(/_/g, '/');
		const decoded = JSON.parse(atob(payload));
		return typeof decoded.plan_key === 'string' ? decoded.plan_key : '';
	} catch {
		return '';
	}
}

export function SandboxCheckoutScreen({
	workspace,
	token,
}: {
	workspace: Workspace;
	token: string;
}) {
	const navigate = useNavigate();
	const plan = planFromToken(token);
	const complete = useMutation({
		mutationFn: (outcome: 'success' | 'failure' | 'cancel') =>
			completeSandbox(workspace.id, token, outcome),
		onSuccess: () =>
			navigate({
				to: '/w/$workspaceSlug/settings/billing',
				params: { workspaceSlug: workspace.slug },
			}),
	});

	if (!token) {
		return (
			<p className="text-sm text-destructive">
				This sandbox checkout is missing its token.
			</p>
		);
	}

	return (
		<section className="max-w-lg space-y-4">
			<header className="space-y-1">
				<h1 className="font-heading text-xl font-semibold">Sandbox checkout</h1>
				<p className="text-sm text-muted-foreground">
					No real money moves. Completing this applies the same webhook path a
					live provider would use.
				</p>
			</header>
			<div className="rounded-lg border p-4 text-sm">
				<p>
					Plan: <span className="font-medium">{plan || 'selected plan'}</span>
				</p>
				<p className="text-muted-foreground">Workspace: {workspace.name}</p>
			</div>
			<div className="flex flex-wrap gap-2">
				<Button
					onClick={() => complete.mutate('success')}
					disabled={complete.isPending}
				>
					Complete sandbox payment
				</Button>
				<Button
					variant="outline"
					onClick={() => complete.mutate('failure')}
					disabled={complete.isPending}
				>
					Simulate failure
				</Button>
			</div>
			{complete.isError ? (
				<p className="text-sm text-destructive">
					Could not complete the sandbox checkout.
				</p>
			) : null}
		</section>
	);
}
