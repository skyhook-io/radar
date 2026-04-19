// Type augmentation for @tanstack/react-query's mutation meta field.
// Used by Radar's binary and by library consumers — imported for side effect
// from main.tsx (standalone) and index.ts (library) so the augmentation is
// present in either compilation path.

declare module '@tanstack/react-query' {
  interface Register {
    mutationMeta: {
      errorMessage?: string;
      successMessage?: string;
      successDetail?: string;
    };
  }
}

export {};
