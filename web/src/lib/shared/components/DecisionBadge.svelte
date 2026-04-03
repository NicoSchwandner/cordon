<script lang="ts">
	import { DECISION_COLORS } from '$lib/shared/api/types';

	let { decision }: { decision: string } = $props();

	const colors = $derived(
		DECISION_COLORS[decision] ?? { bg: 'bg-skeleton', text: 'text-foreground-muted' }
	);
</script>

<span class="inline-block rounded-full px-2 py-0.5 text-xs font-medium {colors.bg} {colors.text}">
	{decision}
</span>
