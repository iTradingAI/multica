import { z } from "zod";

const relativePath = z
  .string()
  .min(1)
  .max(4096)
  .refine(
    (value) =>
      !/[\\:\u0000]/.test(value) &&
      value
        .split("/")
        .every((part) => part !== "" && part !== "." && part !== ".."),
  );
const count = z.number().int().nonnegative();
export const FileTouchSelectionSchema = z
  .object({
    resource_id: z.string().uuid(),
    path: relativePath,
    binding_generation: z
      .number()
      .int()
      .positive()
      .max(Number.MAX_SAFE_INTEGER),
  })
  .strict();
export const IssueFileTouchesSchema = z.object({
  files: z
    .array(
      z.object({
        touch_id: z.string().uuid(),
        resource_id: z.string().uuid(),
        path: relativePath,
        call_count: count,
        last_observed: z.string(),
        operations: z.array(
          z.enum([
            "write",
            "edit",
            "add",
            "update",
            "delete",
            "move_from",
            "move_to",
          ]),
        ),
      }),
    )
    .max(200),
  coverage: z.object({
    unknown: count,
    unavailable: count,
    uncertain: count,
    conflicting: count,
    pending: count,
  }),
  next_cursor: z.union([z.literal(""), z.string().uuid()]),
});
export type IssueFileTouches = z.infer<typeof IssueFileTouchesSchema>;
export type FileTouchSelection = z.infer<typeof FileTouchSelectionSchema>;
