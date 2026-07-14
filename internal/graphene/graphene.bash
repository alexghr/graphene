# Bash completion for Graphene.

_graphene()
{
    local prefix output marker first rest payload candidate current attach
    COMPREPLY=()
    prefix="${COMP_LINE:0:COMP_POINT}"
    output="$(command graphene __complete "$prefix" 2>/dev/null)" || return 0

    marker="__graphene_files__"$'\t'
    case "$output" in
        "$marker"*)
            first="${output%%$'\n'*}"
            if [ "$first" = "$output" ]; then
                rest=""
            else
                rest="${output#*$'\n'}"
            fi
            payload="${first#"$marker"}"
            _graphene_file_attach="${payload%%$'\t'*}"
            _graphene_file_fragment="${payload#*$'\t'}"
            _graphene_file_candidates="$rest"
            complete -o filenames -F _graphene_files graphene gn
            return 124
            ;;
        *)
            current="${prefix##*[[:space:]]}"
            attach=""
            case "$COMP_WORDBREAKS" in
                *"="*)
                    case "$current" in
                        --*=*) attach="${current%%=*}=" ;;
                    esac
                    ;;
            esac
            while IFS= read -r candidate; do
                if [ -n "$attach" ]; then
                    case "$candidate" in
                        "$attach"*) candidate="${candidate#"$attach"}" ;;
                    esac
                fi
                [ -n "$candidate" ] && COMPREPLY[${#COMPREPLY[@]}]="$candidate"
            done < <(printf '%s\n' "$output")
            ;;
    esac
}

_graphene_files()
{
    local attach="$_graphene_file_attach"
    local fragment="$_graphene_file_fragment"
    local candidates="$_graphene_file_candidates"
    local candidate
    COMPREPLY=()
    complete -F _graphene graphene gn
    unset _graphene_file_attach _graphene_file_fragment _graphene_file_candidates

    case "$COMP_WORDBREAKS" in
        *"="*) attach="" ;;
    esac
    while IFS= read -r candidate; do
        [ -n "$candidate" ] && COMPREPLY[${#COMPREPLY[@]}]="${attach}${candidate}"
    done < <(compgen -f -- "$fragment")
    while IFS= read -r candidate; do
        [ -n "$candidate" ] && COMPREPLY[${#COMPREPLY[@]}]="$candidate"
    done < <(printf '%s\n' "$candidates")
}

complete -F _graphene graphene gn
