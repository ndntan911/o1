import { useId, useState, type FormEvent } from 'react';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from '@/components/ui/dialog';
import { Label } from '@/components/ui/label';
import { Textarea } from '@/components/ui/textarea';
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import type { AdminWorkspace } from '@/lib/api/types';
import { useSuspendWorkspace } from './hooks';

export interface SuspendDialogProps {
	workspace: AdminWorkspace;
	onClose: () => void;
}

function unexpectedProblem(): ApiProblem {
	return {
		type: 'about:blank',
		title: 'Something went wrong',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again.',
	};
}

export function SuspendDialog({ workspace, onClose }: SuspendDialogProps) {
	const reasonId = useId();
	const mutation = useSuspendWorkspace();
	const [reason, setReason] = useState('');
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const trimmed = reason.trim();

	function handleSubmit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!trimmed) return;
		setProblem(null);
		mutation.mutate(
			{ workspaceId: workspace.id, reason: trimmed },
			{
				onSuccess: onClose,
				onError: (error) => {
					setProblem(isApiProblem(error) ? error.problem : unexpectedProblem());
				},
			},
		);
	}

	return (
		<Dialog
			open
			onOpenChange={(open) => {
				if (!open) onClose();
			}}
		>
			<DialogContent>
				<form className="space-y-4" onSubmit={handleSubmit}>
					<DialogHeader>
						<DialogTitle>Suspend {workspace.name}</DialogTitle>
						<DialogDescription>
							Members of this workspace will not be able to change monitors
							while it is suspended.
						</DialogDescription>
					</DialogHeader>

					{problem ? <ProblemAlert problem={problem} /> : null}

					<div className="space-y-1">
						<Label htmlFor={reasonId}>Reason</Label>
						<Textarea
							id={reasonId}
							required
							maxLength={500}
							value={reason}
							aria-invalid={problem ? true : undefined}
							onChange={(event) => setReason(event.target.value)}
						/>
						<p className="text-xs text-muted-foreground">{reason.length}/500</p>
					</div>

					<DialogFooter>
						<Button type="button" variant="outline" onClick={onClose}>
							Cancel
						</Button>
						<Button
							type="submit"
							variant="destructive"
							disabled={!trimmed || mutation.isPending}
						>
							Suspend workspace
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
