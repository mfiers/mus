# Configuration Guide

Comprehensive guide to configuring mus for your environment.

## Configuration System

mus uses a **two-tier configuration system**:

1. **Secrets** - Sensitive data (API keys, passwords) stored in system keyring
2. **Environment** - Project metadata stored in `.env` files

## Secrets

### What are Secrets?

Secrets are sensitive credentials that should never be stored in plain text:
- API tokens
- Passwords
- URLs (may contain sensitive info)
- Group names (potentially sensitive)

### Where are Secrets Stored?

**Linux**: `~/.local/share/python_keyring/` (PlaintextKeyring)
**macOS**: System Keychain
**Windows**: Credential Manager

**Security Note**: On Linux, mus uses PlaintextKeyring for performance. The file is still readable only by you (file permissions), but it's not encrypted. For higher security, configure a different keyring backend.

### Managing Secrets

#### Set a Secret

```bash
mus config secret-set KEY VALUE
```

**Examples:**

```bash
# ELN credentials
mus config secret-set eln_apikey "your-api-token-here"
mus config secret-set eln_url "https://your-institution.elabjournal.com"

# iRODS configuration
mus config secret-set irods_home "/tempZone/home/jsmith"
mus config secret-set irods_web "https://mango.example.com/data-object/view"
mus config secret-set irods_group "research_group_alpha"

# macOS iCommands via Docker
mus config secret-set icmd_prefix "docker run --platform linux/amd64 -i --rm -v $HOME:$HOME -v $HOME/.irods/:/root/.irods ghcr.io/utrechtuniversity/docker_icommands:0.2"
```

#### Get a Secret

```bash
mus config secret-get KEY
```

**Examples:**

```bash
mus config secret-get eln_apikey
# Output: your-api-token-here

mus config secret-get irods_home
# Output: /tempZone/home/jsmith
```

#### Delete a Secret

Secrets are stored by the system keyring. To delete:

**Linux (PlaintextKeyring)**:
```bash
# Edit the keyring file
nano ~/.local/share/python_keyring/keyring_pass.cfg

# Or delete entirely
rm ~/.local/share/python_keyring/keyring_pass.cfg
```

**macOS**:
```bash
# Use Keychain Access app
# Search for "mus" entries
# Or use security command
security delete-generic-password -s "mus" -a "eln_apikey"
```

### Environment Variable Override

Secrets can be overridden with environment variables:

```bash
export ELN_APIKEY="override-token"
export ELN_URL="https://different-eln.com"
export IRODS_HOME="/different/zone/home/user"
```

**Priority**: Environment Variable > Keyring > Not Found

**Use case**: Temporary override for testing, CI/CD pipelines, different contexts.

### Required Secrets by Plugin

#### ELN Plugin

| Secret | Required | Description |
|--------|----------|-------------|
| `eln_apikey` | Yes | ELabJournal API token |
| `eln_url` | Yes | ELabJournal base URL |

#### iRODS Plugin

| Secret | Required | Description |
|--------|----------|-------------|
| `irods_home` | Yes | Base path in iRODS |
| `irods_web` | Yes | Mango web interface URL |
| `irods_group` | Yes | iRODS group for permissions |
| `icmd_prefix` | macOS only | Docker prefix for iCommands |

## Environment Files (.env)

### What are .env Files?

`.env` files store non-sensitive configuration in plain text:
- Project metadata
- Experiment IDs
- Tags
- Collaborators

### Format

Simple `KEY=VALUE` format:

```bash
# Comments are allowed
project_name=My Research Project

# Single values
eln_experiment_id=12345
eln_project_id=100

# List values (comma-separated)
tag=analysis,draft,2024
collaborator=alice,bob,charlie
```

### Hierarchical Configuration

`.env` files cascade up the directory tree:

```
/home/jsmith/
    .env                           # Level 1 (root)
        project_name=Lab Work
        collaborator=alice,bob
    │
    └── projects/
        .env                       # Level 2 (projects)
            tag=research_project
        │
        └── cancer-research/
            .env                   # Level 3 (specific project)
                tag=cancer,2024
                eln_project_id=100
            │
            └── experiment-001/
                .env               # Level 4 (experiment)
                    eln_experiment_id=12345
```

**When you run mus from `/home/jsmith/projects/cancer-research/experiment-001/`**:

Configuration is loaded bottom-up:
1. `/home/jsmith/.env`
2. `/home/jsmith/projects/.env`
3. `/home/jsmith/projects/cancer-research/.env`
4. `/home/jsmith/projects/cancer-research/experiment-001/.env`

**Result:**
```python
{
    'project_name': 'Lab Work',
    'collaborator': ['alice', 'bob'],
    'tag': ['research_project', 'cancer', '2024'],
    'eln_project_id': '100',
    'eln_experiment_id': '12345'
}
```

### Key Types

#### Regular Keys (Last Wins)

Most keys follow "last wins" semantics - child overrides parent:

```
# parent/.env
project_name=Old Name

# child/.env
project_name=New Name

# Result: project_name = "New Name"
```

#### List Keys (Accumulate)

Special keys that accumulate values across hierarchy:
- `tag`
- `collaborator`

```
# parent/.env
tag=parent_tag,common_tag

# child/.env
tag=child_tag

# Result: tag = ["parent_tag", "common_tag", "child_tag"]
```

#### List Removal Syntax

Remove values from inherited lists with `-` prefix:

```
# parent/.env
tag=draft,temp,production,test

# child/.env
tag=-draft,-temp

# Result: tag = ["production", "test"]
```

Combine addition and removal:

```
# parent/.env
collaborator=alice,bob,charlie

# child/.env
collaborator=-bob,dave

# Result: collaborator = ["alice", "charlie", "dave"]
```

### Standard Keys

#### ELN Metadata

Created automatically by `mus eln tag-folder`:

| Key | Type | Description |
|-----|------|-------------|
| `eln_experiment_id` | int | ELN experiment ID |
| `eln_experiment_name` | string | ELN experiment name |
| `eln_project_id` | int | ELN project ID |
| `eln_project_name` | string | ELN project name |
| `eln_study_id` | int | ELN study ID |
| `eln_study_name` | string | ELN study name |
| `eln_collaborator` | list | ELN collaborators |

#### Custom Keys

| Key | Type | Description |
|-----|------|-------------|
| `project_name` | string | Your project name |
| `tag` | list | Tags for classification |
| `collaborator` | list | Collaborators on project |

You can add any custom keys you need!

### Managing .env Files

#### View Current Configuration

```bash
mus config show
```

Shows all configuration from current directory up to root.

#### Manually Create .env

```bash
cd ~/projects/my-experiment

# Create .env file
cat > .env << 'EOF'
# Experiment configuration
project_name=Novel Drug Screening
tag=drug_screening,2024,phase1
collaborator=alice,bob
eln_experiment_id=12345
EOF
```

#### Programmatically Update .env

mus doesn't provide direct `.env` editing commands (except `mus eln tag-folder`). Edit manually:

```bash
nano .env
```

Or script it:

```bash
echo "new_key=new_value" >> .env
```

### Best Practices

#### 1. Organize by Hierarchy

```
~/research/                    # Root config
    .env
        collaborator=lab_members

    project-a/                 # Project config
        .env
            eln_project_id=100
            tag=project_a

        experiment-1/          # Experiment config
            .env
                eln_experiment_id=12345
```

#### 2. Don't Store Secrets

```bash
# BAD - Never do this!
echo "eln_apikey=secret123" >> .env

# GOOD - Use keyring
mus config secret-set eln_apikey "secret123"
```

#### 3. Use Tags Effectively

```bash
# Tags help organize and search
tag=manuscript,figure1,published

# Can search by tag later
mus search --tag published
```

#### 4. Version Control .env Files

```bash
# .env files are safe to commit (no secrets!)
git add .env
git commit -m "Add experiment configuration"

# But add to .gitignore if they contain sensitive data
echo "*.env" >> .gitignore
```

#### 5. Document Your Keys

```bash
# .env with comments
# Experiment: Drug screening phase 1
# Started: 2024-03-15
# PI: Dr. Smith
eln_experiment_id=12345
tag=drug_screening,phase1
collaborator=alice,bob,charlie
```

## Configuration Examples

### Simple Single Experiment

```bash
cd ~/experiment
cat > .env << 'EOF'
eln_experiment_id=12345
tag=my_experiment
EOF
```

### Multi-Experiment Project

```
project/
    .env
        eln_project_id=100
        eln_study_id=200
        tag=cancer_research,2024
        collaborator=alice,bob

    experiment-a/
        .env
            eln_experiment_id=12345
            tag=control_group

    experiment-b/
        .env
            eln_experiment_id=12346
            tag=treatment_group
```

### Lab-Wide Configuration

```bash
# ~/research/.env (root for all research)
collaborator=lab_members,pi_name
tag=lab_alpha

# ~/research/project1/.env
eln_project_id=100
tag=project1,grant_xyz

# ~/research/project1/exp001/.env
eln_experiment_id=12345
tag=exp001
# Inherits: lab_alpha,project1,grant_xyz,exp001
```

### Override Parent Tags

```bash
# parent/.env
tag=draft,temp,wip

# child/.env (clean slate)
tag=-draft,-temp,-wip,final,published

# Result: tag = ["final", "published"]
```

## Advanced Configuration

### Custom Keyring Backend

By default, Linux uses PlaintextKeyring. To use encrypted keyring:

```bash
# Install alternative keyring
pip install keyrings.cryptfile

# Configure keyring
export PYTHON_KEYRING_BACKEND=keyrings.cryptfile.cryptfile.CryptFileKeyring

# Set password
export KEYRING_CRYPTFILE_PASSWORD="secure_password"
```

### Multiple ELN Instances

If working with multiple ELN instances:

```bash
# Use environment variables per session
export ELN_URL="https://eln-instance-1.com"
mus eln upload file.txt -m "To instance 1"

export ELN_URL="https://eln-instance-2.com"
mus eln upload file.txt -m "To instance 2"
```

Or use separate directories with different `.env` files.

### macOS Docker Configuration

For iCommands via Docker on macOS:

```bash
# Store once in secrets
mus config secret-set icmd_prefix \
  "docker run --platform linux/amd64 -i --rm \
   -v $HOME:$HOME \
   -v $HOME/.irods/:/root/.irods \
   ghcr.io/utrechtuniversity/docker_icommands:0.2"
```

### Temporary Override

```bash
# Temporarily use different configuration
ELN_EXPERIMENT_ID=99999 mus log -E "Special experiment"

# Or
mus log -E "Message" -x 99999
```

## Configuration Validation

### Check Configuration

```bash
# View all current configuration
mus config show

# Check specific secret
mus config secret-get eln_apikey

# Verify ELN connection (if configured)
mus eln update  # Will fail if credentials wrong
```

### Common Issues

#### Missing Secrets

```
MusSecretNotDefined: eln_apikey not defined
```

**Solution:**
```bash
mus config secret-set eln_apikey "YOUR_KEY"
```

#### Invalid .env Syntax

```
MusInvalidConfigFileEntry: line without =
```

**Solution:** Each line must be `KEY=VALUE` or empty/comment:

```bash
# Good
key=value
# comment
key2=value2

# Bad
this line has no equals sign
```

#### Wrong Keyring Password

```
KeyringError: Failed to unlock keyring
```

**Solution:**
```bash
export KEYRING_CRYPTFILE_PASSWORD="correct_password"
```

## Security Best Practices

### 1. Never Commit Secrets

```bash
# .gitignore
# Don't add .env if it contains secrets
# But mus .env files shouldn't have secrets!

# DO commit
.env  # Contains only metadata

# DON'T commit
secrets.txt
*.key
credentials.json
```

### 2. Protect Keyring

```bash
# Ensure proper permissions
chmod 700 ~/.local/share/python_keyring/
chmod 600 ~/.local/share/python_keyring/*
```

### 3. Use Environment Variables in CI/CD

```yaml
# GitHub Actions example
env:
  ELN_APIKEY: ${{ secrets.ELN_APIKEY }}
  ELN_URL: ${{ secrets.ELN_URL }}

steps:
  - name: Upload to ELN
    run: mus eln upload results.txt -m "CI/CD upload"
```

### 4. Rotate API Keys

Periodically rotate your API keys:

```bash
# 1. Generate new key in ELabJournal
# 2. Update mus
mus config secret-set eln_apikey "NEW_KEY"
# 3. Test
mus eln update
# 4. Revoke old key in ELabJournal
```

### 5. Separate Environments

```bash
# Development
export ELN_URL="https://dev-eln.example.com"

# Production
export ELN_URL="https://eln.example.com"
```

## Configuration Migration

### Moving Between Machines

#### Export Configuration

```bash
# Secrets (manual)
mus config secret-get eln_apikey > secrets.txt
mus config secret-get irods_home >> secrets.txt
# ... etc

# .env files (automatic)
# Just copy them - they're plain text!
cp -r project/ /new/location/
```

#### Import Configuration

```bash
# On new machine
# 1. Set secrets
mus config secret-set eln_apikey "value_from_secrets.txt"
mus config secret-set irods_home "value_from_secrets.txt"

# 2. .env files already copied
# 3. Test
mus config show
```

### Team Configuration

Share `.env` files via Git:

```bash
# Team member A
git add .env
git commit -m "Add project configuration"
git push

# Team member B
git pull
mus config show  # See shared configuration

# But each member sets their own secrets
mus config secret-set eln_apikey "THEIR_KEY"
```

## See Also

- [Installation Guide](installation.md) - Initial setup
- [Quick Start](quickstart.md) - First configuration steps
- [ELN Plugin Guide](eln-plugin.md) - ELN-specific configuration
- [iRODS Plugin Guide](irods-plugin.md) - iRODS-specific configuration
