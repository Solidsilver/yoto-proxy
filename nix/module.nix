self:
{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.yoto-proxy;
  inherit (lib)
    mkEnableOption
    mkIf
    mkOption
    optional
    optionalAttrs
    types
    ;
in
{
  options.services.yoto-proxy = {
    enable = mkEnableOption "yoto-proxy, a bridge from Navidrome to Yoto MYO cards";

    package = mkOption {
      type = types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      description = "The yoto-proxy package.";
    };

    port = mkOption {
      type = types.port;
      default = 3614;
      description = "Port to listen on (loopback only; put a reverse proxy in front).";
    };

    publicUrl = mkOption {
      type = types.str;
      example = "https://yoto.example.com";
      description = ''
        Externally reachable base URL. Yoto players fetch audio from
        <publicUrl>/s/..., so it must be reachable from the internet.
      '';
    };

    dataDir = mkOption {
      type = types.path;
      default = "/var/lib/yoto-proxy";
      description = "Directory for the SQLite database.";
    };

    environmentFile = mkOption {
      type = types.nullOr types.path;
      default = null;
      example = "/run/agenix/yoto-proxy";
      description = ''
        Optional env file (KEY=value lines) loaded by systemd, e.g. an agenix
        secret. It can hold every secret at once:
        YOTO_CLIENT_SECRET, NAVIDROME_PASSWORD, OIDC_CLIENT_SECRET (and any
        other setting). Values set here override the module's own.
      '';
    };

    transcodeBitrate = mkOption {
      type = types.int;
      default = 192;
      description = "mp3 bitrate (kbps) used for tracks that are not already mp3.";
    };

    navidrome = {
      url = mkOption {
        type = types.str;
        default = "http://127.0.0.1:4533";
        description = "Navidrome base URL.";
      };
      auth = mkOption {
        type = types.enum [
          "password"
          "header"
        ];
        default = "password";
        description = ''
          "password": log in as one fixed user (navidrome.user + password).
          "header": act as each logged-in user via Navidrome's external auth.
          Requires Navidrome's ExtAuth.TrustedSources to contain exactly
          navidrome.sourceAddress (and nothing your reverse proxy uses).
        '';
      };
      user = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Navidrome user for password auth.";
      };
      userHeader = mkOption {
        type = types.str;
        default = "Remote-User";
        description = "Header auth: Navidrome's ExtAuth.UserHeader.";
      };
      sourceAddress = mkOption {
        type = types.str;
        default = "127.0.0.2";
        description = ''
          Header auth: loopback address yoto-proxy connects to Navidrome from.
          Trust only this address in ExtAuth.TrustedSources, never 127.0.0.1,
          which the reverse proxy uses for internet traffic.
        '';
      };
      userMap = mkOption {
        type = types.attrsOf types.str;
        default = { };
        example = {
          luke = "solidsilver";
        };
        description = "Header auth: login (Pocket ID) username -> Navidrome username, where they differ.";
      };
      passwordFile = mkOption {
        type = types.nullOr types.path;
        default = null;
        description = "File containing the Navidrome user's password (or set NAVIDROME_PASSWORD in environmentFile).";
      };
    };

    audiobookshelf = {
      publicUrl = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "https://shelf.example.com";
        description = ''
          Browser-facing Audiobookshelf URL; setting it enables audiobooks.
          Users connect with Audiobookshelf's OpenID login, so add
          <publicUrl of yoto-proxy>/auth/abs/callback to Audiobookshelf's
          "Allowed Mobile Redirect URIs".
        '';
      };
      url = mkOption {
        type = types.str;
        default = "http://127.0.0.1:13378";
        description = "Audiobookshelf API URL used by the server.";
      };
      bitrate = mkOption {
        type = types.int;
        default = 64;
        description = "mp3 bitrate (kbps, mono) for audiobook chapters that must be re-encoded.";
      };
    };

    yoto = {
      clientId = mkOption {
        type = types.str;
        description = "Client ID of the app registered at dashboard.yoto.dev.";
      };
      clientSecretFile = mkOption {
        type = types.nullOr types.path;
        default = null;
        description = "Client secret file, if the Yoto app is a confidential client (or set YOTO_CLIENT_SECRET in environmentFile).";
      };
    };

    oidc = {
      issuer = mkOption {
        type = types.str;
        description = "OIDC issuer URL (e.g. Pocket ID).";
      };
      clientId = mkOption {
        type = types.str;
        description = "OIDC client ID.";
      };
      clientSecretFile = mkOption {
        type = types.nullOr types.path;
        default = null;
        description = "File containing the OIDC client secret (or set OIDC_CLIENT_SECRET in environmentFile).";
      };
    };
  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.navidrome.auth != "password" || cfg.navidrome.user != null;
        message = "services.yoto-proxy.navidrome.user is required when navidrome.auth = \"password\".";
      }
      {
        assertion = cfg.navidrome.auth != "header" || cfg.navidrome.sourceAddress != "127.0.0.1";
        message = "services.yoto-proxy.navidrome.sourceAddress must not be 127.0.0.1 (the reverse proxy's address).";
      }
    ];

    users.users.yoto-proxy = {
      isSystemUser = true;
      group = "yoto-proxy";
      home = cfg.dataDir;
    };
    users.groups.yoto-proxy = { };

    systemd.tmpfiles.rules = [ "d ${cfg.dataDir} 0750 yoto-proxy yoto-proxy - -" ];

    systemd.services.yoto-proxy = {
      description = "yoto-proxy";
      wantedBy = [ "multi-user.target" ];
      after = [
        "network-online.target"
        "navidrome.service"
        "audiobookshelf.service"
      ];
      wants = [ "network-online.target" ];
      unitConfig.RequiresMountsFor = cfg.dataDir;

      environment = {
        LISTEN_ADDR = "127.0.0.1:${toString cfg.port}";
        PUBLIC_URL = cfg.publicUrl;
        DATA_DIR = cfg.dataDir;
        TRANSCODE_BITRATE = toString cfg.transcodeBitrate;
        FFMPEG_PATH = "${pkgs.ffmpeg-headless}/bin/ffmpeg";
        ABS_URL = cfg.audiobookshelf.url;
        ABS_BITRATE = toString cfg.audiobookshelf.bitrate;
        NAVIDROME_URL = cfg.navidrome.url;
        NAVIDROME_AUTH = cfg.navidrome.auth;
        YOTO_CLIENT_ID = cfg.yoto.clientId;
        OIDC_ISSUER = cfg.oidc.issuer;
        OIDC_CLIENT_ID = cfg.oidc.clientId;
      }
      // optionalAttrs (cfg.audiobookshelf.publicUrl != null) {
        ABS_PUBLIC_URL = cfg.audiobookshelf.publicUrl;
      }
      // optionalAttrs (cfg.navidrome.user != null) {
        NAVIDROME_USER = cfg.navidrome.user;
      }
      // optionalAttrs (cfg.navidrome.auth == "header") {
        NAVIDROME_USER_HEADER = cfg.navidrome.userHeader;
        NAVIDROME_SOURCE_ADDR = cfg.navidrome.sourceAddress;
        NAVIDROME_USER_MAP = lib.concatStringsSep "," (
          lib.mapAttrsToList (k: v: "${k}=${v}") cfg.navidrome.userMap
        );
      }
      // optionalAttrs (cfg.navidrome.passwordFile != null) {
        NAVIDROME_PASSWORD_FILE = "%d/navidrome-password";
      }
      // optionalAttrs (cfg.oidc.clientSecretFile != null) {
        OIDC_CLIENT_SECRET_FILE = "%d/oidc-client-secret";
      }
      // optionalAttrs (cfg.yoto.clientSecretFile != null) {
        YOTO_CLIENT_SECRET_FILE = "%d/yoto-client-secret";
      };

      serviceConfig = {
        ExecStart = lib.getExe cfg.package;
        User = "yoto-proxy";
        Group = "yoto-proxy";
        Restart = "on-failure";
        RestartSec = "5s";
        # Read by systemd as root before dropping privileges, so the agenix
        # file can stay root-only.
        EnvironmentFile = lib.optional (cfg.environmentFile != null) cfg.environmentFile;
        LoadCredential =
          optional (cfg.navidrome.passwordFile != null) "navidrome-password:${cfg.navidrome.passwordFile}"
          ++ optional (cfg.oidc.clientSecretFile != null) "oidc-client-secret:${cfg.oidc.clientSecretFile}"
          ++ optional (cfg.yoto.clientSecretFile != null) "yoto-client-secret:${cfg.yoto.clientSecretFile}";

        ProtectSystem = "strict";
        ProtectHome = true;
        ReadWritePaths = [ cfg.dataDir ];
        PrivateTmp = true;
        PrivateDevices = true;
        NoNewPrivileges = true;
        ProtectProc = "invisible";
        ProcSubset = "pid";
        ProtectKernelModules = true;
        ProtectKernelTunables = true;
        ProtectControlGroups = true;
        RestrictAddressFamilies = [
          "AF_INET"
          "AF_INET6"
          "AF_UNIX"
        ];
        CapabilityBoundingSet = "";
        SystemCallFilter = "@system-service";
        MemoryDenyWriteExecute = true;
        UMask = "0027";
      };
    };
  };
}
