import type { IssuedUserPassword, Role, UserSummary } from "@ksb/api-client";
import { queryOptions, useMutation } from "@tanstack/react-query";
import { api, failure } from "./api";
import { queryClient } from "./auth";

export const usersQuery = queryOptions({
  queryKey: ["users"],
  queryFn: async (): Promise<UserSummary[]> => {
    const { data, error, response } = await api.GET("/api/v1/users");
    if (error) throw failure(error, response);
    return data;
  },
});

function invalidateUsers() {
  return queryClient.invalidateQueries({ queryKey: usersQuery.queryKey });
}

export function useCreateUser(onCreated: (issued: IssuedUserPassword) => void) {
  return useMutation({
    mutationFn: async (body: { username: string; role: Role }) => {
      const { data, error, response } = await api.POST("/api/v1/users", { body });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: (issued) => {
      void invalidateUsers();
      onCreated(issued);
    },
  });
}

export type UserAction = "promote" | "demote" | "disable" | "enable" | "reset" | "unlink" | "delete";

async function runAction(action: UserAction, id: number): Promise<IssuedUserPassword | undefined> {
  const path = { params: { path: { id } } };
  const update = async (body: { role?: Role; disabled?: boolean }) => {
    const { error, response } = await api.PATCH("/api/v1/users/{id}", { ...path, body });
    if (error) throw failure(error, response);
    return undefined;
  };
  switch (action) {
    case "promote":
      return update({ role: "admin" });
    case "demote":
      return update({ role: "user" });
    case "disable":
      return update({ disabled: true });
    case "enable":
      return update({ disabled: false });
    case "reset": {
      const { data, error, response } = await api.POST("/api/v1/users/{id}/password", path);
      if (error) throw failure(error, response);
      return data;
    }
    case "unlink": {
      const { error, response } = await api.DELETE("/api/v1/users/{id}/oidc", path);
      if (error) throw failure(error, response);
      return undefined;
    }
    case "delete": {
      const { error, response } = await api.DELETE("/api/v1/users/{id}", path);
      if (error) throw failure(error, response);
      return undefined;
    }
  }
}

export function useUserAction(action: UserAction, id: number, onDone: (issued?: IssuedUserPassword) => void) {
  return useMutation({
    mutationFn: () => runAction(action, id),
    onSuccess: (issued) => {
      void invalidateUsers();
      // Устройства удалённого пользователя уходят вместе с ним.
      if (action === "delete") void queryClient.invalidateQueries({ queryKey: ["devices"] });
      onDone(issued);
    },
  });
}
