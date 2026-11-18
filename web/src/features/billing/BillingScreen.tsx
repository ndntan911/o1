import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import type { Workspace } from '@/lib/api/types';
import { queryKeys } from '@/lib/query/keys';
import { cancelSubscription, completeSandbox, startCheckout } from './api';
import { useBilling, usePlans } from './hooks';

function when(value: string | null): string {
	if (!value) return '—';
	return new Date(value).toLocaleDateString(undefined, {
		year: 'numeric',
		month: 'short',
		day: 'numeric',
	});
}

export function BillingScreen({ workspace }: { workspace: Workspace }) {
	const queryClient = useQueryClient();
	const billing = useBilling(workspace.id);
	const plans = usePlans();
	const [sandboxToken, setSandboxToken] = useState<string | null>(null);
	const [sandboxPlan, setSandboxPlan] = useState('');

	const checkout = useMutation({
		mutationFn: (plan: string) => startCheckout(workspace.id, plan),
		onSuccess: ({ url }) => {
			const token = new URL(url, window.location.origin).searchParams.get(
				'token',
			);
			setSandboxToken(token);
		},
	});
	const complete = useMutation({
		mutationFn: (outcome: 'success' | 'failure') =>
			completeSandbox(workspace.id, sandboxToken ?? '', outcome),
		onSuccess: async () => {
			setSandboxToken(null);
			await queryClient.invalidateQueries({
				queryKey: queryKeys.billing(workspace.id),
			});
		},
	});
	const cancel = useMutation({
		mutationFn: () => cancelSubscription(workspace.id),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: queryKeys.billing(workspace.id),
			});
		},
	});

	if (billing.isLoading) {
		return <p className="text-sm text-muted-foreground">Loading billing…</p>;
	}
	if (billing.isError || !billing.data) {
		return <p className="text-sm text-destructive">Could not load billing.</p>;
	}

	const current = billing.data;
	const isFree = current.plan.key === 'free';
	const upgradePlans = (plans.data?.plans ?? []).filter(
		(plan) => plan.key !== 'free',
	);

	return (
		<section className="space-y-6">
			<header className="flex items-start justify-between gap-4">
				<div>
					<h1 className="font-heading text-xl font-semibold">Billing</h1>
					<p className="text-sm text-muted-foreground">
						Plan, usage limits, and renewal for {workspace.name}.
					</p>
				</div>
				{current.mode === 'sandbox' ? (
					<span className="rounded-full border px-3 py-1 text-xs text-muted-foreground">
						Sandbox mode
					</span>
				) : null}
			</header>

			<div className="rounded-lg border p-4">
				<div className="flex flex-wrap items-baseline justify-between gap-2">
					<h2 className="font-heading text-lg font-semibold">
						{current.plan.name}
					</h2>
					<span className="text-sm text-muted-foreground">
						{current.status}
						{current.trial_end
							? ` · trial ends ${when(current.trial_end)}`
							: ''}
					</span>
				</div>
				<dl className="mt-4 grid grid-cols-2 gap-4 text-sm sm:grid-cols-4">
					<div>
						<dt className="text-muted-foreground">Monitors</dt>
						<dd className="font-medium">{current.plan.monitors}</dd>
					</div>
					<div>
						<dt className="text-muted-foreground">Alert channels</dt>
						<dd className="font-medium">{current.plan.channels}</dd>
					</div>
					<div>
						<dt className="text-muted-foreground">API keys</dt>
						<dd className="font-medium">{current.plan.api_keys}</dd>
					</div>
					<div>
						<dt className="text-muted-foreground">Renews</dt>
						<dd className="font-medium">
							{current.cancel_at_period_end
								? `ends ${when(current.current_period_end)}`
								: when(current.current_period_end)}
						</dd>
					</div>
				</dl>
			</div>

			{sandboxToken ? (
				<div className="space-y-3 rounded-lg border border-dashed p-4">
					<div>
						<h2 className="font-heading text-lg font-semibold">
							Sandbox checkout
						</h2>
						<p className="text-sm text-muted-foreground">
							No real money moves. Completing this applies the same webhook path
							a live provider would use.
						</p>
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
				</div>
			) : null}

			{isFree ? (
				<div className="space-y-3">
					<h2 className="font-heading text-lg font-semibold">Upgrade</h2>
					<div className="flex flex-wrap gap-2">
						{upgradePlans.map((plan) => (
							<Button
								key={plan.key}
								onClick={() => {
									setSandboxPlan(plan.key);
									checkout.mutate(plan.key);
								}}
								disabled={checkout.isPending}
							>
								Upgrade to {plan.name}
							</Button>
						))}
					</div>
					{checkout.isError ? (
						<p className="text-sm text-destructive">
							Could not start checkout.
						</p>
					) : null}
				</div>
			) : (
				<div className="flex flex-wrap gap-2">
					<Button
						variant="outline"
						onClick={() => cancel.mutate()}
						disabled={current.cancel_at_period_end || cancel.isPending}
					>
						{current.cancel_at_period_end
							? 'Cancellation scheduled'
							: 'Cancel at period end'}
					</Button>
				</div>
			)}

			{sandboxPlan && sandboxToken ? (
				<p className="text-xs text-muted-foreground">
					Selected plan: {sandboxPlan}
				</p>
			) : null}
		</section>
	);
}
