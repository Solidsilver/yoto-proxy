{
  lib,
  buildGoModule,
  stdenvNoCC,
  nodejs,
  pnpm,
  fetchPnpmDeps,
  pnpmConfigHook,
}:

let
  version = "0.1.0";

  # The SvelteKit frontend, built to static files and embedded in the binary.
  web = stdenvNoCC.mkDerivation (finalAttrs: {
    pname = "yoto-proxy-web";
    inherit version;
    src = lib.cleanSourceWith {
      src = ../web;
      filter =
        path: _type:
        !(builtins.elem (baseNameOf path) [
          "node_modules"
          "build"
          ".svelte-kit"
          "embed.go"
        ]);
    };
    nativeBuildInputs = [
      nodejs
      pnpm
      pnpmConfigHook
    ];
    pnpmDeps = fetchPnpmDeps {
      inherit (finalAttrs) pname version src;
      fetcherVersion = 5;
      hash = "sha256-yfjMgHtz9Gu2sh5zoq7wsyCc72864/ksP9OcBvF/w6c=";
    };
    buildPhase = ''
      runHook preBuild
      pnpm build
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      cp -r build $out
      runHook postInstall
    '';
  });
in
buildGoModule {
  pname = "yoto-proxy";
  inherit version;
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
      ../web/embed.go
    ];
  };
  vendorHash = "sha256-jnjwqopGdB3A4XVN3rlGtQ91cbbu+64rYYMQOWQlWCM=";
  subPackages = [ "cmd/yoto-proxy" ];
  ldflags = [
    "-s"
    "-w"
  ];
  preBuild = ''
    rm -rf web/build
    cp -r ${web} web/build
    chmod -R u+w web/build
  '';
  # Module vendoring does not need the frontend.
  overrideModAttrs = _: { preBuild = ""; };

  passthru = { inherit web; };
  meta = {
    description = "Serve Navidrome playlists and albums to Yoto Make Your Own cards";
    mainProgram = "yoto-proxy";
  };
}
