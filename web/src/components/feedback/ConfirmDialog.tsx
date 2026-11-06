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
import type { ApiProblem } from '@/lib/api/problems';

export interface ConfirmDialogProps {
	open: boolean;
	title: string;
	description?: string;
	confirmLabel: string;
	pending?: boolean;
	problem?: ApiProblem | null;
	onConfirm: () => void;
	onOpenChange: (open: boolean) => void;
}

export function ConfirmDialog({
	open,
	title,
	description,
	confirmLabel,
	pending = false,
	problem,
	onConfirm,
	onOpenChange,
}: ConfirmDialogProps) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>{title}</DialogTitle>
					{description ? (
						<DialogDescription>{description}</DialogDescription>
					) : null}
				</DialogHeader>
				{problem ? <ProblemAlert problem={problem} /> : null}
				<DialogFooter>
					<Button
						type="button"
						variant="outline"
						onClick={() => onOpenChange(false)}
						disabled={pending}
					>
						Cancel
					</Button>
					<Button
						type="button"
						variant="destructive"
						onClick={onConfirm}
						disabled={pending}
					>
						{confirmLabel}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
