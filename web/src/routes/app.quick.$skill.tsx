import { createFileRoute } from '@tanstack/react-router';
import { Quick } from '../pages/app';

export const Route = createFileRoute('/app/quick/$skill')({
  component: () => {
    const { skill } = Route.useParams();
    return skill === 'daily-observation' ? (
      <Quick key={skill} skill="stock_analysis" daily />
    ) : (
      <Quick key={skill} skill={skill as 'stock_analysis' | 'capital_flow' | 'deep_research'} />
    );
  },
});
